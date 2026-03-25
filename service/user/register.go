package user

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/audit"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster/routes"
	"github.com/cloudreve/Cloudreve/v4/pkg/email"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gin-gonic/gin"
)

// RegisterParameterCtx define key fore UserRegisterService
type RegisterParameterCtx struct{}

// UserRegisterService 管理用户注册的服务
type UserRegisterService struct {
	UserName string `form:"username" json:"username" binding:"required,min=3,max=100"`
	Email    string `form:"email" json:"email" binding:"required,email"`
	Password string `form:"password" json:"password" binding:"required,min=6,max=128"`
	Language string `form:"language" json:"language"`
}

// Register 新用户注册
func (service *UserRegisterService) Register(c *gin.Context) serializer.Response {
	dep := dependency.FromContext(c)
	settings := dep.SettingProvider()

	isEmailRequired := settings.EmailActivationEnabled(c)
	args := &inventory.NewUserArgs{
		Username:      service.UserName,
		Email:         strings.ToLower(service.Email),
		PlainPassword: service.Password,
		Status:        user.StatusActive,
		GroupID:       settings.DefaultGroup(c),
		Language:      service.Language,
	}
	if isEmailRequired {
		args.Status = user.StatusInactive
	}

	userClient := dep.UserClient()
	uc, tx, _, err := inventory.WithTx(c, userClient)
	if err != nil {
		return serializer.DBErr(c, "Failed to start transaction", err)
	}

	expectedUser, err := uc.Create(c, args)
	if expectedUser != nil {
		util.WithValue(c, inventory.UserCtx{}, expectedUser)
	}

	if err != nil {
		_ = inventory.Rollback(tx)
		if errors.Is(err, inventory.ErrUserEmailExisted) {
			return serializer.ErrWithDetails(c, serializer.CodeEmailExisted, "Email already in use", err)
		}
		if errors.Is(err, inventory.ErrUserUsernameExisted) {
			return serializer.ErrWithDetails(c, serializer.CodeParamErr, "Username already in use", err)
		}

		if errors.Is(err, inventory.ErrInactiveUserExisted) {
			if err := sendActivationEmail(c, dep, expectedUser); err != nil {
				return serializer.ErrWithDetails(c, serializer.CodeNotSet, "", err)
			}

			return serializer.ErrWithDetails(c, serializer.CodeEmailSent, "User is not activated, activation email has been resent", nil)
		}

		return serializer.DBErr(c, "Failed to insert user row", err)
	}

	settingClient := dep.SettingClient().SetClient(uc.GetClient()).(inventory.SettingClient)
	if err := disableOpenRegistrationAfterFirstSignup(c, settingClient, expectedUser); err != nil {
		_ = inventory.Rollback(tx)
		return serializer.DBErr(c, "Failed to update registration setting", err)
	}

	if err := inventory.Commit(tx); err != nil {
		return serializer.DBErr(c, "Failed to commit user row", err)
	}
	if err := invalidateOpenRegistrationCache(dep.KV(), expectedUser); err != nil {
		dep.Logger().Warning("Failed to clear registration setting cache after signup: %s", err)
	}

	if err := audit.Publish(c, &audit.Event{
		Type:   audit.UserSignup,
		UserID: expectedUser.ID,
		Content: map[string]any{
			"account":  userUsernameValue(expectedUser.Username),
			"username": userUsernameValue(expectedUser.Username),
		},
	}); err != nil {
		dep.Logger().Warning("Failed to publish signup audit log: %s", err)
	}

	if isEmailRequired {
		if err := sendActivationEmail(c, dep, expectedUser); err != nil {
			return serializer.ErrWithDetails(c, serializer.CodeNotSet, "", err)
		}
		return serializer.Response{Code: serializer.CodeNotFullySuccess}
	}

	return serializer.Response{Data: BuildUser(expectedUser, dep.HashIDEncoder())}
}

func sendActivationEmail(ctx context.Context, dep dependency.Dep, newUser *ent.User) error {
	base := dep.SettingProvider().SiteURL(ctx)
	userID := hashid.EncodeUserID(dep.HashIDEncoder(), newUser.ID)
	ttl := time.Now().Add(time.Duration(24) * time.Hour)
	activateURL, err := auth.SignURI(ctx, dep.GeneralAuth(), routes.MasterUserActivateAPIUrl(base, userID).String(), &ttl)
	if err != nil {
		return serializer.NewError(serializer.CodeEncryptError, "Failed to sign the activation link", err)
	}

	// 取得签名
	credential := activateURL.Query().Get("sign")

	// 生成对用户访问的激活地址
	finalURL := routes.MasterUserActivateUrl(base)
	queries := finalURL.Query()
	queries.Add("id", userID)
	queries.Add("sign", credential)
	finalURL.RawQuery = queries.Encode()

	// 返送激活邮件
	title, body, err := email.NewActivationEmail(ctx, dep.SettingProvider(), newUser, finalURL.String())
	if err != nil {
		return serializer.NewError(serializer.CodeFailedSendEmail, "Failed to send activation email", err)
	}

	if err := dep.EmailClient(ctx).Send(ctx, newUser.Email, title, body); err != nil {
		return serializer.NewError(serializer.CodeFailedSendEmail, "Failed to send activation email", err)
	}

	return nil
}

// ActivateUser 激活用户
func ActivateUser(c *gin.Context) serializer.Response {
	uid := hashid.FromContext(c)
	dep := dependency.FromContext(c)
	userClient := dep.UserClient()

	// 查找待激活用户
	inactiveUser, err := userClient.GetByID(c, uid)
	if err != nil {
		return serializer.ErrWithDetails(c, serializer.CodeUserNotFound, "User not fount", err)
	}

	// 检查状态
	if inactiveUser.Status != user.StatusInactive {
		return serializer.ErrWithDetails(c, serializer.CodeUserCannotActivate, "This user cannot be activated", nil)
	}

	// 激活用户
	activeUser, err := userClient.SetStatus(c, inactiveUser, user.StatusActive)
	if err != nil {
		return serializer.DBErr(c, "Failed to update user", err)
	}

	if err := audit.Publish(c, &audit.Event{
		Type:   audit.UserActivated,
		UserID: activeUser.ID,
		Content: map[string]any{
			"account":  userUsernameValue(activeUser.Username),
			"username": userUsernameValue(activeUser.Username),
		},
	}); err != nil {
		dep.Logger().Warning("Failed to publish activation audit log: %s", err)
	}

	util.WithValue(c, inventory.UserCtx{}, activeUser)
	return serializer.Response{Data: BuildUser(activeUser, dep.HashIDEncoder())}
}
