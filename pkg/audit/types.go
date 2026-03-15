package audit

const (
	ServerStart = iota
	UserSignup
	EmailSent
	UserActivated
	UserLoginFailed
	UserLogin
	UserTokenRefresh
	FileCreate
	FileRename
	SetFilePermission
	EntityUploaded
	EntityDownloaded
	CopyFrom
	CopyTo
	MoveTo
	DeleteFile
	MoveToTrash
	Share
	ShareLinkViewed
	SetCurrentVersion
	DeleteVersion
	ThumbGenerated
	LivePhotoUploaded
	UpdateMetadata
	EditShare
	DeleteShare
	Mount
	Relocate
	CreateArchive
	ExtractArchive
	WebdavLoginFailed
	WebdavAccountCreate
	WebdavAccountUpdate
	WebdavAccountDelete
	PaymentCreated
	PointsChange
	PaymentPaid
	PaymentFulfilled
	PaymentFulfillFailed
	StorageAdded
	GroupChanged
	UserExceedQuotaNotified
	UserChanged
	GetDirectLink
	LinkAccount
	UnlinkAccount
	ChangeNick
	ChangeAvatar
	MembershipUnsubscribe
	ChangePassword
	Enable2FA
	Disable2FA
	AddPasskey
	RemovePasskey
	RedeemGiftCode
	FileImported
	UpdateView
	DeleteDirectLink
	ReportAbuse
	OAuthGrantCreate
	OAuthTokenExchange
	OAuthGrantRevoke
)

func AllEventTypes() []int {
	res := make([]int, OAuthGrantRevoke+1)
	for i := range res {
		res[i] = i
	}

	return res
}
