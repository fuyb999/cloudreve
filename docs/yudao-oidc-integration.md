# Cloudreve 与 Yudao OIDC 统一认证接入说明

本文档说明当前版本 `Cloudreve` 如何接入 `Yudao / 芋道` 的 OIDC 统一认证，并给出可直接落地的配置项、联调步骤、登录/校验/刷新/登出时序以及常见问题排查方式。

## 1. 目标与边界

本次接入的目标不是做两套 Token 的双向签发，而是统一成一个认证中心：

- `Yudao` 作为唯一的 OIDC Provider / Token Issuer。
- `Cloudreve` 作为 OIDC Client，同时也是消费 `Bearer Token` 的资源服务端。
- 当 `Cloudreve` 开启 OIDC 后，前端最终持有的是 `Yudao` 签发的 `access_token / refresh_token / id_token`，而不是再额外生成一套 Cloudreve 自己的登录 Token。
- `Cloudreve` 服务端会在首次命中时校验 `Yudao Token`，并把校验结果和本地影子用户映射缓存下来；在 Token 有效期内，后续请求优先走本地校验，不需要每次远程访问 `Yudao`。
- 当 `Cloudreve` 后台的 `参数设置 -> 用户会话 -> OIDC` 开关关闭时，系统完全回退到原有本地登录逻辑，不影响旧流程。

可以把这套方案理解为：

1. 认证统一到 `Yudao`
2. `Cloudreve` 保留本地用户实体，但只作为影子用户与业务归属载体
3. 单点登录、单点退出、Token 撤销广播由 `Yudao` 负责

## 2. 总体架构

当前推荐架构如下：

- `Yudao Backend`：提供 OIDC Discovery、授权码换票、userinfo、token introspection、logout、back-channel logout、token revoke callback。
- `Yudao UI`：提供浏览器可直接访问的统一登录授权页入口 `/sso`。
- `Cloudreve Frontend`：在登录页展示或自动跳转到统一认证入口，接收 `/session/oidc/callback` 回调。
- `Cloudreve Backend`：负责授权码换票、同步本地影子用户、校验 Bearer Token、刷新 Token、处理登出和广播失效。

当前实现里，`Cloudreve` 对 `Yudao Token` 的使用链路分为 5 条：

1. 登录链路：浏览器跳转到 `Yudao`，授权码换票后返回 `Yudao Token`
2. Bearer Token 校验链路：首次远程 introspection + userinfo，之后走本地缓存
3. 刷新链路：使用 `refresh_token` 调用 Provider 刷新
4. 主动登出链路：本地登出后调用 revoke，再跳转 `end_session_endpoint`
5. 广播失效链路：接收 `revokeCallback` 和 `Back-Channel Logout`，立即失效本地缓存

## 3. 关键代码位置

### 3.1 Cloudreve 侧

- 配置模型：`pkg/setting/types.go`
- 配置读取：`pkg/setting/provider.go`
- 管理后台 OIDC 配置界面：`assets/src/component/Admin/Settings/UserSession/SSOSettings.tsx`
- OIDC 登录准备与换票：`service/user/oidc.go`
- OIDC Token 校验、刷新、登出、广播失效：`service/user/oidc_token.go`
- 路由注册：`routers/router.go`

### 3.2 Yudao 侧

- Discovery 端点：`yudao-module-system/.../controller/oidc/OidcDiscoveryController.java`
- Logout 端点：`yudao-module-system/.../controller/oidc/OidcLogoutController.java`
- Back-Channel Logout 广播：`yudao-module-system/.../service/oidc/OidcBackChannelLogoutServiceImpl.java`
- `id_token` 扩展 Claim：`yudao-module-system/.../service/oidc/OidcIdTokenServiceImpl.java`
- OAuth2 Client UI：`yudao-ui-admin-vue3/src/views/system/oauth2/client/ClientForm.vue`
- `/sso` 页面路由：`yudao-ui-admin-vue3/src/router/modules/remaining.ts`
- `/sso` 页面实现：`yudao-ui-admin-vue3/src/views/Login/components/SSOLogin.vue`

## 4. 接入前准备

接入前请先确认下面 6 项：

1. `Cloudreve` 的站点 URL 已正确配置为最终访问地址，否则回调地址、退出回跳地址都会错误。
2. `Yudao Backend` 的 `issuer`、Discovery 暴露地址、对外访问域名一致。
3. `Yudao UI` 可以通过浏览器直接访问 `/sso`。
4. `Cloudreve` 可以访问 `Yudao` 的 discovery、token、userinfo、check-token、logout 端点。
5. 生产环境应使用 `HTTPS`，并确保双方机器时间同步。
6. 如果启用了多租户，`Yudao` 需要在 `id_token` 中带上 `tenant_id`，用于退出时恢复租户上下文。

## 5. Cloudreve 端配置

### 5.1 后台入口

进入：

`参数设置 -> 用户会话 -> OIDC`

当前版本对应的配置键如下：

| 界面字段 | 配置键 | 说明 |
| --- | --- | --- |
| OIDC 开关 | `oidc_enabled` | 打开后走统一认证；关闭后恢复旧登录逻辑 |
| 显示名称 | `oidc_display_name` | 登录页按钮文案，例如 `Yudao SSO` |
| 自动跳转到统一认证 | `oidc_auto_redirect` | 打开登录页后是否自动发起 OIDC 登录 |
| SSO 地址 | `oidc_sso_url` | 推荐填写 `Yudao UI` 的 `/sso` 地址 |
| Well-Known URL | `oidc_wellknown_url` | Discovery 地址 |
| Client ID | `oidc_client_id` | 与 `Yudao OAuth2 Client` 保持一致 |
| Client Secret | `oidc_client_secret` | 与 `Yudao OAuth2 Client` 保持一致 |
| Scope | `oidc_scope` | 推荐至少包含 `openid user_info user.read` |

推荐配置示例：

```text
oidc_enabled = 1
oidc_display_name = Yudao SSO
oidc_auto_redirect = 0
oidc_sso_url = https://auth.example.com/sso
oidc_wellknown_url = https://auth-api.example.com/.well-known/openid-configuration
oidc_client_id = cloudreve
oidc_client_secret = <your-client-secret>
oidc_scope = openid user_info user.read
```

### 5.2 为什么 `SSO 地址` 推荐填 `/sso`

`Cloudreve` 在生成统一认证跳转地址时，优先使用：

1. `oidc_sso_url`
2. discovery 里的 `authorization_endpoint`
3. `issuer + /sso`

对于当前 `Yudao` 实现，推荐直接填写：

```text
https://<yudao-ui-host>/sso
```

原因是：

- `Yudao UI` 的 `/sso` 已经接好了浏览器授权页流程。
- 当前 `Yudao` 的用户登录体验是以 UI 路由页为入口，而不是直接把浏览器打到一个纯后端渲染的授权页。
- `Cloudreve` 会把标准 OIDC 参数拼到这个地址后面，包括 `client_id`、`redirect_uri`、`response_type`、`state`、`scope`、`code_challenge` 等。

如果未来 `Yudao` 完全标准化到纯 OIDC 授权入口，也可以不填 `SSO 地址`，让 `Cloudreve` 直接使用 discovery 的 `authorization_endpoint`。

### 5.3 Cloudreve 回调地址

当前前端固定展示的回调地址为：

```text
https://<cloudreve-host>/session/oidc/callback
```

这个地址需要注册到 `Yudao OAuth2 Client` 的 `redirectUris` 中。

### 5.4 登出和失效通知地址

推荐同时准备以下两个地址：

```text
Back-Channel Logout:
https://<cloudreve-host>/api/v4/session/oidc/backchannelLogout

Token Revoke Callback:
https://<cloudreve-host>/api/v4/session/oidc/revokeCallback
```

其中：

- `backchannelLogout` 是标准 OIDC Back-Channel Logout 回调
- `revokeCallback` 是业务增强版的 Token 撤销通知，适合更快清掉本地缓存

## 6. Yudao 端配置

### 6.1 创建 OAuth2 Client

需要在 `Yudao` 中为 `Cloudreve` 创建一个 OAuth2 Client，至少保证以下字段正确：

- `clientId`：与 `Cloudreve oidc_client_id` 一致
- `secret`：与 `Cloudreve oidc_client_secret` 一致
- `redirectUris`：必须包含 `https://<cloudreve-host>/session/oidc/callback`
- `scope`：建议至少包含 `openid`、`user.read`

### 6.2 `additionalInformation` 推荐值

当前 `Yudao UI` 提供了 `additionalInformation` 文本框，可用于存放 OIDC 扩展元数据。推荐填写：

```json
{
  "backchannel_logout_uri": "https://<cloudreve-host>/api/v4/session/oidc/backchannelLogout",
  "post_logout_redirect_uris": [
    "https://<cloudreve-host>/session"
  ]
}
```

字段说明：

- `backchannel_logout_uri`：统一登出时，`Yudao` 广播到 `Cloudreve` 的回调地址
- `post_logout_redirect_uris`：允许登出后跳回 `Cloudreve /session`

### 6.3 `tokenRevokeCallbackUrl`

`Cloudreve` 已支持 `POST /api/v4/session/oidc/revokeCallback`，用于接收 `Yudao` 的 Token 撤销回调。

需要注意：

- `Yudao Backend` 已有 `tokenRevokeCallbackUrl` 字段和回调实现
- 但当前 `Yudao UI` 的 `OAuth2 Client` 表单里还没有单独暴露这个字段
- 因此这一项通常需要通过 `Yudao` 管理接口、数据库脚本或补 UI 的方式写入

推荐值：

```text
https://<cloudreve-host>/api/v4/session/oidc/revokeCallback
```

### 6.4 `redirectUris` 如何填写

最稳妥的做法是：

1. 在 `redirectUris` 中加入：

```text
https://<cloudreve-host>/session/oidc/callback
```

2. 在 `additionalInformation.post_logout_redirect_uris` 中加入：

```text
https://<cloudreve-host>/session
```

当前 `Yudao` 的登出控制器还支持“与已注册 `redirectUri` 同源”的兼容匹配，所以即使 `post_logout_redirect_uri` 和登录回调路径不同，只要同源，也可以通过校验。

## 7. 标准端点对照

当前 `Yudao Discovery` 暴露的关键端点如下：

| 能力 | 端点 |
| --- | --- |
| Discovery | `/.well-known/openid-configuration` |
| Authorization | `/admin-api/system/oauth2/authorize` |
| Token | `/admin-api/system/oauth2/token` |
| UserInfo | `/admin-api/system/oauth2/user/get` |
| Introspection | `/admin-api/system/oauth2/check-token` |
| End Session | `/oidc/logout` |
| JWKS | `/.well-known/jwks.json` |

需要额外说明两点：

1. `Yudao Discovery` 当前把 `revocation_endpoint` 暴露为 `/admin-api/system/oauth2/token`
2. `Cloudreve` 已内置兼容逻辑：会先尝试标准 POST revoke；如果失败，再回退到 `DELETE /admin-api/system/oauth2/token?token=...`

因此目前不需要额外修改 `Cloudreve` 端代码即可兼容 `Yudao` 的撤销实现。

## 8. 完整链路说明

### 8.1 登录链路

登录流程如下：

1. 用户打开 `Cloudreve` 登录页
2. 如果 `oidc_enabled=1` 且开启自动跳转，则前端直接请求 `GET /api/v4/session/oidc/prepare`
3. `Cloudreve` 后端读取 OIDC 配置、拉取 discovery、生成 `state` 和 `PKCE code_verifier`
4. 后端返回拼好的统一认证地址
5. 浏览器跳转到 `Yudao /sso`
6. 用户在 `Yudao` 登录并完成授权
7. `Yudao` 回调到 `https://<cloudreve-host>/session/oidc/callback?code=...&state=...`
8. `Cloudreve` 前端把 `code/state` 提交给 `POST /api/v4/session/oidc/exchange`
9. `Cloudreve` 后端调用 `token_endpoint` 换取 `access_token / refresh_token / id_token`
10. `Cloudreve` 再调用 `userinfo_endpoint` 拉取用户资料
11. `Cloudreve` 根据 `issuer + subject` 同步或创建本地影子用户
12. `Cloudreve` 返回本地用户资料和 Provider Token 给前端

这里有两个关键点：

- `Cloudreve` 登录成功后拿到的 Token 是 `Yudao Token`
- 本地用户只是影子用户，用于保存文件归属、容量、审计等本地业务数据

### 8.2 Bearer Token 校验链路

`Cloudreve` 对 Bearer Token 的校验分两段：

第一段，首次命中：

1. 请求带着 `Authorization: Bearer <access_token>` 进入 `Cloudreve`
2. `Cloudreve` 判断 OIDC 开关已打开
3. 先查本地缓存是否已有该 Token 的校验结果
4. 如果没有，则调用 `Yudao introspection`
5. 再调用 `Yudao userinfo`
6. 根据 `issuer + sub` 同步本地影子用户
7. 把 `access_token -> local_user_id`、过期时间、签发时间、subject 等信息写入缓存

第二段，后续命中：

1. 相同 `access_token` 再次访问 `Cloudreve`
2. 命中本地缓存后，直接恢复本地用户 ID
3. 不再每次远程请求 `Yudao`

这就是“统一认证 + 本地高性能校验”的核心。

### 8.3 刷新链路

当 `access_token` 过期后：

1. 前端使用 `refresh_token`
2. 调用 `Cloudreve` 的刷新接口
3. `Cloudreve` 后端透传到 `Yudao token_endpoint`
4. `Yudao` 返回新的 Token
5. `Cloudreve` 把新的 Provider Token 回给前端

这条链路仍然以 `Yudao` 为唯一 Token 签发方。

### 8.4 主动登出链路

当前实现的主动登出流程如下：

1. 用户在 `Cloudreve` 点击退出
2. `Cloudreve` 先尝试调用 Provider revoke，撤销当前 `access_token` 或 `refresh_token`
3. `Cloudreve` 本地把 access token 标记为已撤销，避免短时间内继续使用
4. `Cloudreve` 生成统一认证中心的退出地址：

```text
<end_session_endpoint>
  ?client_id=<client-id>
  &id_token_hint=<id-token>
  &post_logout_redirect_uri=https://<cloudreve-host>/session
```

5. 浏览器跳转到 `Yudao /oidc/logout`
6. `Yudao` 清理中心侧 Token，会广播 Back-Channel Logout，并最终跳回 `Cloudreve /session`

### 8.5 广播失效链路

当前推荐同时启用两种失效通知：

#### 方式一：Token Revoke Callback

适合在 Token 被撤销时快速通知 `Cloudreve`：

1. `Yudao` 撤销 Token
2. `Yudao` 通过 `tokenRevokeCallbackUrl` 发 HTTP POST 回调
3. `Cloudreve` 校验时间戳与签名
4. `Cloudreve` 把该 Token 标记为已撤销

#### 方式二：Back-Channel Logout

适合做标准化的“全应用退出”广播：

1. 某个应用发起统一退出
2. `Yudao` 生成签名的 `logout_token`
3. `Yudao` 对所有已配置 `backchannel_logout_uri` 的客户端发起回调
4. `Cloudreve` 验证 `logout_token` 的签名、issuer、audience、events
5. `Cloudreve` 根据 `issuer + subject` 标记该用户已退出
6. 对应用户的本地缓存不再认为之前的 Token 可用

两种方式的职责不同：

- `revokeCallback` 更关注“某一个 access token 是否失效”
- `Back-Channel Logout` 更关注“某个用户会话在所有客户端上都失效”

建议两者同时保留。

## 9. Cloudreve 本地用户如何承接 Yudao 用户

当前推荐策略是“影子用户模式”：

1. `Yudao` 是用户主数据源
2. `Cloudreve` 本地仍保留 `user` 表，用于承接文件、容量、审计、任务归属等业务数据
3. `Cloudreve` 使用 `external_identity` 之类的外部身份映射，把 `issuer + subject` 对应到本地用户
4. 登录或校验 Token 时，如本地不存在映射，则创建影子用户；存在时同步基础资料

这样做的优点是：

- 不需要把 `Cloudreve` 的全部业务结构强行搬进 `Yudao`
- 文件归属仍然稳定落在本地数据库
- 后续如果把部门、用户组、公共文件权限下沉到 `Yudao`，只需要把“认证”和“授权判断”继续外置，不需要重做本地文件模型

## 10. 多租户注意事项

### 10.1 为什么之前会出现 `TenantContextHolder 不存在租户编号`

统一退出场景里，请求可能不是从浏览器正常携带租户头进入，而是从其他应用、认证中心或后端回调直接打到 `Yudao /oidc/logout`。

如果这时 `Yudao` 退出逻辑仍依赖当前线程里的租户上下文，就会出现：

```text
TenantContextHolder 不存在租户编号
```

### 10.2 当前解决方式

当前 `Yudao` 退出逻辑已经增强为：

- 优先从 `id_token_hint` 中解析 `tenant_id`
- 如果能拿到 `tenant_id`，则切回对应租户执行退出
- 如果拿不到，则使用忽略租户上下文的方式执行统一登出

同时，`id_token` 中已经补充了 `tenant_id` Claim，用于支撑这条链路。

## 11. 常见问题排查

### 11.1 点击退出后跳到 `http://localhost:48080/`

通常说明以下几项之一有问题：

1. `Yudao issuer` 仍是默认本地地址
2. `Cloudreve` 的 `Well-Known URL` 指向了错误环境
3. `Cloudreve` 站点 URL 没有配置成真实对外地址
4. `post_logout_redirect_uri` 没在 `Yudao` 的允许范围内

排查顺序建议：

1. 先打开 `/.well-known/openid-configuration` 看 `issuer` 和 `end_session_endpoint`
2. 再检查 `Cloudreve` 后台的 `Well-Known URL`
3. 再检查 `OAuth2 Client` 的 `redirectUris` 和 `additionalInformation.post_logout_redirect_uris`

### 11.2 `SSO 地址` 填成了后端接口，授权体验异常

当前 `Yudao` 推荐填：

```text
https://<yudao-ui-host>/sso
```

不要直接把 `SSO 地址` 理解成 discovery 里的某个后端 JSON 接口地址。

### 11.3 登出后没有跳回 Cloudreve 登录页

重点检查：

- `post_logout_redirect_uri` 是否为 `https://<cloudreve-host>/session`
- `additionalInformation.post_logout_redirect_uris` 是否包含该地址
- 或者该地址是否与已注册 `redirectUri` 同源

### 11.4 Cloudreve 仍然每次远程校验 Token

正常情况下不会。

如果仍然频繁远程访问，通常说明：

- Token 缓存未命中
- Token 被撤销回调标记失效
- 用户已收到 Back-Channel Logout
- 本地缓存 TTL 过短或缓存层异常

### 11.5 `revokeCallback` 没有生效

重点检查：

- `Yudao OAuth2 Client` 是否设置了 `tokenRevokeCallbackUrl`
- 回调签名是否使用了当前 `client_secret`
- 双方机器时间是否同步
- `Cloudreve` 是否已启用 OIDC

## 12. 与标准 OIDC 的兼容性

当前这套实现虽然是为 `Yudao` 优先适配的，但整体思路并没有锁死在 `Yudao` 上。

只要后续 OIDC Provider 能提供以下能力，`Cloudreve` 就可以较平滑接入：

1. Discovery
2. Authorization Code + PKCE
3. Token Endpoint
4. UserInfo Endpoint
5. Introspection Endpoint
6. End Session Endpoint
7. 最好支持 Back-Channel Logout

需要注意的是：

- 并不是所有标准 OIDC Provider 都默认开放 `introspection`
- 也不是所有 Provider 都支持 `Back-Channel Logout`
- 如果缺失这些能力，`Cloudreve` 仍可做登录，但在“本地快速校验”和“统一退出广播”上会退化

因此，当前方案是：

- 对 `Yudao` 走完整增强版链路
- 对其他标准 OIDC Provider，按能力做兼容接入

这意味着以后如果接入更标准的 OIDC，也不需要推倒重做。

## 13. 与后续公共文件权限治理的衔接建议

如果后续你要把“公共文件目录权限、部门可见性、群组可见性、操作级权限”也统一交给 `Yudao`，建议采用下面的边界划分：

- `Yudao` 负责用户、部门、群组、组织关系和权限 AST 下发
- `Cloudreve` 负责文件树、`owner_id`、`tree_path`、存储、索引和具体文件操作执行
- `Cloudreve` 本地只缓存“可见条件 AST”和“操作裁决结果”
- 每次查询文件时，把远程下发的 AST 转换为 `ent` 条件和 `ES` 查询条件
- 每次上传、下载、删除、重命名等操作时，再根据远程裁决或本地短缓存做拦截

这样以后统一认证和统一授权会保持同一个控制面，后续演进成本最低。

## 14. 联调检查清单

最终联调前，请按下面的最小清单逐项确认：

1. `Cloudreve` 后台已开启 `OIDC`
2. `Cloudreve oidc_wellknown_url` 可访问
3. `Cloudreve oidc_sso_url` 指向 `Yudao UI /sso`
4. `Cloudreve` 站点 URL 已配置为真实对外地址
5. `Yudao OAuth2 Client.redirectUris` 包含 `https://<cloudreve-host>/session/oidc/callback`
6. `Yudao additionalInformation.backchannel_logout_uri` 已配置
7. `Yudao additionalInformation.post_logout_redirect_uris` 已配置
8. `Yudao tokenRevokeCallbackUrl` 已配置
9. `Yudao Discovery` 返回的 `issuer`、`token_endpoint`、`userinfo_endpoint`、`end_session_endpoint` 均正确
10. 使用浏览器完成一次登录，确认 `Cloudreve` 能成功进入首页
11. 用 `Bearer Token` 连续访问多次接口，确认不是每次都远程校验
12. 执行退出，确认浏览器最终回到 `Cloudreve /session`
13. 在任一接入应用退出，确认 `Cloudreve` 中旧 Token 立即失效

完成以上 13 项后，这套 `Cloudreve <-> Yudao` OIDC 单点登录闭环就算落通了。
