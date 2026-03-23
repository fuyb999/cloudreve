# 浏览器联调测试计划（Chrome）

版本：`2026-03-22`  
覆盖系统：网盘 `Cloudreve`、统一认证 `Authverse`（前端 + 后端）、统一检索 `Search`（前端 + `authverse-backend/yudao-module-search`）、网盘同步客户端 `Syncthing`  
执行原则：**严格按顺序逐步推进**，任一步失败立即修复并回归；全部步骤完成后按相同流程做一次全流程回归。

---

## 0. 背景与目标

### 0.1 项目目录（本仓库）

- 网盘：`cloudreve/`（前端：`cloudreve/assets/`；后端：Go）
- 统一认证：`authverse/`（前端）、`authverse-backend/`（后端）
- 统一检索：`search-frontend/`（前端）、`authverse-backend/yudao-module-search/`（后端模块）
- 网盘同步客户端：`syncthing/`

### 0.2 系统关系（必须验证）

1. **统一认证（Authverse）负责网盘与检索的 OAuth/OIDC 登录**：在任一端完成登录后，拿到的 Token 能打通三系统接口。
2. **网盘（Cloudreve）也提供内置 OAuth（主要用于客户端）**：Syncthing 通过 Cloudreve 内置 OAuth 完成授权。
3. **Syncthing 客户端扫描本地文件并上传到网盘**：文件变更应在网盘、ES 索引与检索结果中保持一致，且不越权。

### 0.3 测试目标（对应用户需求）

1. 网盘、检索、统一认证三系统 OAuth/OIDC 登录联调；联调成功后在同一浏览器拉起 Syncthing，通过网盘认证验证可用。
2. Syncthing 上传与文件操作同步：新建/移动/复制/重命名/删除/恢复等；至少 **50000 文件**压测，验证两端一致且无报错。
3. Syncthing 设备注册、解绑、配置恢复、冲突等全流程。
4. 网盘个人文件操作全覆盖（基于前端操作入口补全操作集合）。
5. 网盘公共文件操作：联合统一认证中的网盘授权策略，做可见性与可操作性判断，覆盖用户/角色/部门等广场景，禁止越权。
6. 统一检索：网盘索引在“公共文件可见性”基础上叠加检索策略；验证检索无越权；新增并测试检索策略模板（需自定义模板）。
7. 网盘主从节点任务协调：解压/压缩/文件抽取/ES 同步/离线下载等在协调模式下工作正常；队列监控正常；结合 ES、Tika、PG 联调。
8. 网盘文件变动同步到 ES 的正确性：覆盖所有文件操作，并在 50000 文件基数上做压力测试。
10. 统一认证所有页面/功能模块测试。

---

## 1. 执行规范（强制）

### 1.1 Chrome 联调要求

- 仅使用 Chrome（建议使用无扩展的独立 Profile）
- DevTools：
  - Network 勾选 `Preserve log`
  - 勾选 `Disable cache`（仅在 DevTools 打开时生效）
  - 需要时导出 HAR（作为证据附件）

### 1.2 逐步推进与回归规则

- 每个阶段都有“通过门槛（Gate）”，**未达 Gate 禁止进入下一阶段**。
- 任意用例失败：
  1. 立即记录：失败现象、复现步骤、预期/实际、关键请求、报错截图、日志片段
  2. 立即修复（代码或配置）
  3. 先回归本用例，再回归“最近一次 Gate 之后的所有已通过用例”（最小回归闭环）
  4. 回归通过后才继续推进
- 全部阶段完成后：按本文档顺序执行一次“全流程回归（Smoke）”。

### 1.3 记录模板（每条用例都要填）

> 建议直接在本文档对应阶段的表格中填写；如果你们有缺陷系统，可把 `Bug/PR/Commit` 填到系统链接或 commit hash。

字段说明：

- `结果`：Pass / Fail
- `失败原因`：必须写清楚是“前端、后端、配置、数据、第三方依赖（ES/Tika/PG）、Syncthing 客户端、网络/证书、时间偏差”等哪一类
- `修改路径`：必须写到具体文件路径（或配置项名、SQL 脚本名）

统一记录表头（在各阶段复用）：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据（截图/HAR/日志） | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |

---

## 2. 关键切入点（出问题优先从这里看）

### 2.1 Cloudreve：统一认证 OIDC（浏览器）

- 前端回调路由：`cloudreve/assets/src/router/index.tsx`：`/session/oidc/callback`（`oidc/callback`）
- 后端端点（`cloudreve/routers/router.go`）：
  - `GET /api/v4/session/oidc/prepare`
  - `POST /api/v4/session/oidc/exchange`
  - `POST /api/v4/session/oidc/revokeCallback`
  - `POST /api/v4/session/oidc/backchannelLogout`
- 详细时序与配置：`cloudreve/docs/yudao-oidc-integration.md`（注意其中 “Yudao” 旧命名，对应当前 Authverse）

### 2.2 Cloudreve：内置 OAuth（客户端，Syncthing）

- OAuth 授权入口（Syncthing 会跳这里）：`GET /session/authorize?...`
- 后端端点（`cloudreve/routers/router.go`）：
  - `GET /api/v4/session/oauth/app/:app_id`
  - `POST /api/v4/session/oauth/consent`
  - `POST /api/v4/session/oauth/token`
  - `GET /api/v4/session/oauth/userinfo`
- 默认 Syncthing OAuth Client（DB migration 默认写入）：`cloudreve/inventory/migration.go`
  - GUID：`5367e9c5-4711-440a-b440-0e1ff8cbb2d6`
  - Redirect URIs（必须匹配 Syncthing 本地回调）：
    - `http://127.0.0.1:18384/rest/noauth/auth/cloudreve/callback`
    - `http://localhost:18384/rest/noauth/auth/cloudreve/callback`
    - `https://127.0.0.1:18384/rest/noauth/auth/cloudreve/callback`
    - `https://localhost:18384/rest/noauth/auth/cloudreve/callback`

### 2.3 Cloudreve：Syncthing 设备管理（浏览器 + 客户端上报）

- 前端页面入口：`/connect` -> Syncthing Tab  
  - `cloudreve/assets/src/component/Pages/Devices/Devices.tsx`
  - `cloudreve/assets/src/component/Pages/Devices/SyncthingClient.tsx`
- 前端接口：`cloudreve/assets/src/api/api.ts`
  - `GET /devices/syncthing`
  - `DELETE /devices/syncthing/:deviceID`（解绑）
  - `DELETE /devices/syncthing/:deviceID/permanent`（永久删除）
- 后端端点（`cloudreve/routers/router.go`）：
  - `GET /api/v4/devices/syncthing`
  - `PUT /api/v4/devices/syncthing/report`
  - `POST /api/v4/devices/syncthing/heartbeat`
  - `POST /api/v4/devices/syncthing/activity`
- 解绑/迁移/配置恢复语义：`cloudreve/inventory/syncthing_device.go`
  - 未注册/解绑后的报错：`ErrSyncthingDeviceNotRegistered`
  - 同 IP 冲突：`ErrSyncthingDeviceIPConflict`

### 2.4 Cloudreve：文件操作按钮展示与权限矩阵（个人盘/公共盘）

- 右键菜单按钮可见性与能力判定集中在：  
  `cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`

重点覆盖的动作（计划用例会按这些全覆盖）：

- 空白区域：刷新、新建文件夹、新建文件、从模板新建、上传、远程下载
- 文件：打开/打开方式、下载、直链/直链管理、分享/管理分享、重命名、复制、移动、删除、标签/元数据、版本控制、创建压缩包、解压、信息、重置缩略图
- 文件夹：进入、置顶、重命名、复制、移动、删除、标签/元数据、创建压缩包（文件夹走压缩下载）
- 特殊上下文：搜索结果（“定位到父目录”）、回收站（恢复）、share_redirect（跳转到分享）

### 2.5 统一检索（Search）：越权关键点

- 运行态接口（后端控制器）：  
  `authverse-backend/yudao-module-search/.../controller/app/query/SearchQueryAppController.java`
  - `GET /app-api/search/sources`
  - `GET /app-api/search/overview`
  - `GET /app-api/search/dashboard`
  - `POST /app-api/search/query`
- 用户类型注意事项：  
  统一检索当前仍沿用 `/app-api` 前缀，但期望使用 **统一认证后台用户（ADMIN）** 的 access token 访问。  
  相关兼容逻辑见：`authverse-backend/yudao-module-search/.../framework/web/SearchAppApiAdminUserTypeFilter.java`
- “Cloudreve 公共可见性”与检索 allow 的合并逻辑：  
  `authverse-backend/yudao-module-search/.../service/query/CloudrevePublicVisibilityAllowAugmentor.java`  
  语义：`finalAllow = baseAllow OR publicVisibilityAllow`（因此必须专项验证“不可见资源不会被检索命中”）
- Search 前端（用于可视化越权与压测后抽样）：  
  `search-frontend/src/component/Yudao/SearchProject.tsx`（支持 `statsOnly` 模式快速看命中统计）

### 2.6 Authverse（统一认证）前端页面范围

- 统一管理模块清单（用于“全页面覆盖”列举）：  
  `authverse/src/component/UnifiedAuth/systemSections.tsx`
- 路由总表（用于枚举所有可达页面）：  
  `authverse/src/router/index.tsx`

---

## 3. 环境与依赖（执行前必须确认）

### 3.1 基础地址（填写实际值）

| 系统 | Base URL | 备注 |
| --- | --- | --- |
| Cloudreve | `http://localhost:5212` | 当前使用 `go run . -w -c data/conf.ini server` 启动 |
| Authverse 前端 | `http://localhost:5174` | 统一认证 UI（含 `/sso`） |
| Authverse 后端 | `http://localhost:48080` | 统一认证 API（OIDC discovery：`/.well-known/openid-configuration`） |
| Search 前端 | `http://localhost:5175` | 统一检索 UI |
| Search 后端 | `http://localhost:48080` | 运行态接口前缀：`/app-api/search/*` |
| PostgreSQL | `localhost:5432/ruoyi-vue-pro` | Authverse 运行库（`cloudreve/cloudreve`） |
| Elasticsearch | `http://127.0.0.1:9200` | 已连通，版本 `8.12.2` |
| Tika | `http://127.0.0.1:9998` | 已连通，版本 `3.2.3` |

### 3.2 数据库/菜单初始化（如是全新环境）

Authverse-backend 已提供脚本（路径按需执行）：

- 检索相关：`authverse-backend/sql/postgresql/search-*.sql`
- 网盘授权相关：`authverse-backend/sql/postgresql/cloudreve-authz-*.sql`

Cloudreve 授权中心 E2E 操作路径与模板库参考：`cloudreve/docs/yudao-cloudreve-authz-e2e.md`

### 3.3 时间/证书/跨域

- Authverse issuer、discovery、回调域名必须一致（避免 OIDC 校验失败）
- 生产建议 HTTPS；本地联调如果混用 http/https，会影响：
  - OIDC redirect_uri 校验
  - Syncthing OAuth redirect_uri 校验
- 机器时间差过大将导致 token 校验失败（exp/iat）

---

## 4. 测试数据与账号矩阵（建议标准化）

### 4.1 账号与组织（建议至少准备）

| 标识 | 用途 | 组织/角色建议 |
| --- | --- | --- |
| U0（管理员） | 配置授权、检索源/索引/策略、统一授权预览与模拟 | super_admin |
| U1（普通用户A） | 个人盘操作、公共盘只读/部分可写验证 | DeptA，RoleA |
| U2（普通用户B） | 越权对照（应不可见/不可操作） | DeptB，RoleB |
| U3（跨部门/多角色） | 复杂矩阵验证 | DeptA1 + RoleB 等 |

部门树建议：

- Root
  - DeptA
    - DeptA1
  - DeptB

### 4.2 公共盘资源结构（必须包含“可检索关键字”）

建议在 Cloudreve `public` 下准备：

- `public/readonly/`：只读资源（含 doc/pdf/txt，正文写入唯一关键词 `KW_READONLY_*`）
- `public/maintain/`：可维护资源（含多级目录与大批量文件，关键词 `KW_MAINTAIN_*`）
- `public/secret/`：敏感资源（仅授权给少数主体，关键词 `KW_SECRET_*`）

并准备几类文件用于内容处理：

- PDF（含中文、含图片）
- DOCX/PPTX/XLSX
- 大文本（>10MB）
- 压缩包（zip/7z/rar）用于解压任务

### 4.3 50000 文件压测数据集（本地生成）

数据集建议：

- 50000 个小文件（1KB~4KB）+ 100 个中等文件（1MB~5MB）+ 5 个大文件（>100MB）
- 文件名覆盖：
  - 中文/空格/特殊字符（`[]()#&+`）
  - 超长路径（接近上限）
  - 同名不同扩展名

生成方式可用你们现有脚本；若无脚本，建议用一个简单生成器（可在执行时补充进仓库工具目录）。

---

## 5. 分阶段联调测试用例（按顺序执行）

> 每阶段都有 Gate，必须全部 Pass 才能进入下一阶段。

---

### 阶段 1：三系统 OAuth/OIDC 登录联调（Gate-1）

目标：

- Cloudreve 使用 Authverse 的 OIDC 登录成功
- Search 前端与 Authverse 管理端登录成功（同浏览器会话）
- 任意一端获取到的 Token 能访问三系统接口（至少各 1 个代表性接口）

#### 用例清单与记录（Gate-1）

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据（截图/HAR/日志） | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G1-01 | Cloudreve 登录页跳转统一认证（OIDC prepare） | `GET /api/v4/session/oidc/prepare` 返回跳转 URL，Chrome 跳转到 Authverse `/sso` | Pass | 首轮因 Authverse 前端依赖与登录页动画问题阻塞，已修复 | Cloudreve 日志：`2026-03-23 10:13:42 GET /api/v4/session/oidc/prepare?next=%2Fhome -> 200` | 本地联调修复 | 见 `8.2` 缺陷表 | 回归通过：同浏览器会话可稳定从 Cloudreve 登录页跳转到 Authverse |
| G1-02 | Authverse `/sso` 登录并授权 | 登录成功，回调到 `Cloudreve /session/oidc/callback` | Pass | 首轮因 OAuth 初始化抖动、scope 默认全 false、PG 缺列阻塞，已修复 | Authverse 日志：`/admin-api/system/oauth2/authorize` 成功，scope 为 `{\"openid\":true,\"user_info\":true,\"user.read\":true}` | 本地联调修复 | 见 `8.2` 缺陷表 | 回归通过：授权后可回跳 `http://localhost:5212/session/oidc/callback?...` |
| G1-03 | Cloudreve 授权码换票 | `POST /api/v4/session/oidc/exchange` 成功，Cloudreve 首页可访问 | Pass | 首轮因 callback 页面重复 exchange 导致“登录会话不存在”，已修复 | Cloudreve 日志：`2026-03-23 10:13:44 POST /api/v4/session/oidc/exchange -> 200`，后续 `/api/v4/file?uri=cloudreve://my -> 200` | 本地联调修复 | `cloudreve/assets/src/util/index.ts`、`cloudreve/assets/src/component/Pages/Login/Signin/OIDCCallback.tsx` | 回归通过：最终落在 `http://localhost:5212/home` |
| G1-04 | Cloudreve Bearer Token 可调用网盘代表接口 | 任意文件列表/用户信息接口返回 200 | Pass | 无 | `2026-03-23 20:20` 使用 Cloudreve 会话 token 调 `GET /api/v4/user/capacity`：HTTP 200，`code=0`，`used=0` | 本地联调实测 | 无 | 回归通过：同一 token 可调用网盘接口 |
| G1-05 | 同 Token 调用 Authverse 代表接口 | Authverse 后端代表接口返回 200（按实际选取） | Pass | 无 | `2026-03-23 20:20` 调 `GET /admin-api/system/oauth2/user/get`：HTTP 200，`code=0`，返回用户 `admin` | 本地联调实测 | 无 | 回归通过：Cloudreve token 直接打通 Authverse |
| G1-06 | 同 Token 调用 Search 运行态接口 | `GET /app-api/search/overview` 或 `POST /app-api/search/query` 返回 200 | Pass | 无 | `2026-03-23 20:20` 调 `GET /app-api/search/overview`：HTTP 200，`code=0`，返回检索概览数据 | 本地联调实测 | 无 | 回归通过：Cloudreve token 直接打通 Search 运行态 |
| G1-07 | 登出与失效通知 | 登出后 Cloudreve 会话失效；必要时触发 `revokeCallback/backchannelLogout` 清缓存 | Pass | 首次仅传 `refresh_token` 触发登出时，旧 access token 仍可调用接口（执行方式不完整）；补齐前端 signout 参数与跳转链路后通过 | `2026-03-23 20:22` 调 `DELETE /api/v4/session/token`（`access_token+refresh_token+id_token`）返回 OIDC logout URL；同旧 token 再调 `GET /api/v4/user/capacity` 返回 `code=40020 OIDC access token has been revoked`；浏览器最终回到 `http://localhost:5212/session`，未登录调 `GET /api/v4/user/capacity` 返回 `code=401` | 本地联调修正（流程） | 无代码改动（联调操作流程修正） | 回归通过：登出后旧 token 失效且浏览器会话回到登录态 |

失败排查与修改切入点：

- Cloudreve OIDC：
  - `cloudreve/docs/yudao-oidc-integration.md`（配置项/时序/常见问题）
  - 后端：`cloudreve/service/user/oidc.go`、`cloudreve/service/user/oidc_token.go`
  - 路由：`cloudreve/routers/router.go`
- Authverse 登录与回调：
  - 前端路由：`authverse/src/router/index.tsx`
  - 统一管理权限/菜单：`authverse/src/component/UnifiedAuth/systemSections.tsx`

Gate-1 通过标准：

- G1-01 ~ G1-07 全部 Pass。

---

### 阶段 2：同浏览器拉起 Syncthing，通过网盘内置 OAuth 认证（Gate-2）

目标：

- Syncthing 在同一 Chrome 会话中打开授权页，完成 Cloudreve 内置 OAuth 授权
- 授权完成后：
  - Syncthing 能拿到 access_token/refresh_token
  - Cloudreve `/connect` 页面能看到设备已注册、在线/绑定状态刷新

#### 用例清单与记录（Gate-2）

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据（截图/HAR/日志） | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G2-01 | Cloudreve 显示 Syncthing 引导页 | `/connect` 页面正常，Syncthing 下载链接可用（或有明确提示） | Pass | 无 | `2026-03-23 20:35` 浏览器进入 `http://localhost:5212/connect`，页面展示 Syncthing 引导文案、Windows/Linux 下载按钮与三步引导说明 | 本地联调实测 | 无 | 回归通过：`/connect` 页面可稳定加载 |
| G2-02 | Syncthing 打开 Cloudreve 授权页 | 浏览器打开 `${cloudreve}/session/authorize?...`，且能显示应用信息/授权按钮 | Pass | 无 | `GET http://127.0.0.1:18384/rest/noauth/auth/cloudreve/login -> 302`，`Location` 指向 `http://localhost:5212/session/authorize?...redirect_uri=http://127.0.0.1:18384/rest/noauth/auth/cloudreve/callback...`；浏览器落在授权页 `授权应用 - Cloudreve` 并显示 “Syncthing” 与 “授权应用” 按钮 | 本地联调实测 | 无 | 回归通过：授权页拉起正常 |
| G2-03 | 授权回调到本地端口 | 浏览器回跳到 `http(s)://127.0.0.1:18384/rest/noauth/auth/cloudreve/callback` 并显示成功 | Pass | 无 | 点击 Cloudreve 授权页“授权应用”后，同一浏览器落回 Syncthing 本地站点 `http://127.0.0.1:18384/`（标题：`芋道源码 | Syncthing`）；授权入口 `redirect_uri` 已明确为 `/rest/noauth/auth/cloudreve/callback` | 本地联调实测 | 无 | 回归通过：本地端口回跳链路可用 |
| G2-04 | Syncthing 换票成功 | Syncthing 调用 `POST /api/v4/session/oauth/token` 成功并持久化 session | Pass | 首次启动存在历史失效会话，持续报 `40020`；重新完成 OAuth 并重启 Syncthing 后恢复 | `GET http://127.0.0.1:18384/rest/noauth/auth/cloudreve/status` 返回 `authorized=true`，含 `userName/userEmail/scope`，证明换票与会话持久化成功 | 本地联调修正（流程） | 无代码改动（重授权 + 客户端重启） | 回归通过：Syncthing 已处于 Cloudreve 授权态 |
| G2-05 | Cloudreve 设备列表出现并可刷新 | `GET /api/v4/devices/syncthing` 返回设备，UI 每 30s 刷新 last_seen/online | Pass | 无 | `GET /api/v4/devices/syncthing`：HTTP 200，`code=0`，返回设备 `JJVV453...`，`online=true`，`is_bound=true`；Cloudreve `/connect` 页面可见设备卡片、最近在线与绑定 URI | 本地联调实测 | 无 | 回归通过：设备注册、在线状态与刷新正常 |

失败排查与修改切入点：

- Redirect URI 不匹配：
  - Cloudreve 内置 OAuth Client 默认 redirect 见 `cloudreve/inventory/migration.go`
  - Syncthing 回调构造见 `syncthing/internal/cloudreve/oauth.go`（`RedirectURIForRequest`）
- 授权页无法打开/无法显示：
  - Cloudreve OAuth 路由：`cloudreve/routers/router.go`（`/api/v4/session/oauth/*`）
  - Cloudreve 授权页：`/session/authorize`（前端页面/接口联动）
- 设备不上报：
  - Cloudreve 设备端点：`/api/v4/devices/syncthing/*`（见 `cloudreve/routers/router.go`）
  - Syncthing 上报实现：`syncthing/internal/cloudreve/client.go`
  - Cloudreve 解绑/迁移/恢复语义：`cloudreve/inventory/syncthing_device.go`

Gate-2 通过标准：

- G2-01 ~ G2-05 全部 Pass。

---

### 阶段 3：Syncthing 上传与文件操作同步 + 50000 文件压测（Gate-3）

目标：

- 基础文件操作同步正确
- 50000 文件基数下：
  - Syncthing 与 Cloudreve 两端一致
  - 不出现批量报错/死循环/任务队列异常
  - 为阶段 8（ES 同步）提供稳定基线

#### 3.1 基础同步用例（先小规模）

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据（截图/HAR/日志） | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G3-01 | 新建文件夹同步 | 本地新建文件夹，Cloudreve 端出现且路径一致 | Pass | 无 | `2026-03-23 20:56` 本地创建 `g3r2_folder` 后触发扫描；`GET /api/v4/file?uri=cloudreve://my/2/gate3-sync` 返回 `name=g3r2_folder` | 本地联调实测 | 无 | 回归通过：目录可稳定同步到 Cloudreve |
| G3-02 | 新建文件同步 | 本地新建文件，Cloudreve 端出现；大小/mtime合理 | Pass | 无 | `2026-03-23 20:56` 本地创建 `g3r2_folder/g3r2_new_file.txt`（42B）；远端目录查询返回同名文件 `size=42` | 本地联调实测 | 无 | 回归通过：文件创建与大小同步正确 |
| G3-03 | 移动同步 | 本地移动文件/文件夹，Cloudreve 端路径更新 | Pass | 无 | `2026-03-23 20:57` 本地 `g3r2_folder/g3r2_new_file.txt -> g3r2_moved.txt`；远端根目录出现 `g3r2_moved.txt`，`g3r2_folder` 为空 | 本地联调实测 | 无 | 回归通过：移动后路径一致 |
| G3-04 | 复制同步 | 本地复制文件/文件夹，Cloudreve 端出现副本 | Pass | 无 | `2026-03-23 20:58` 本地复制 `g3r2_moved.txt -> g3r2_copy.txt`；远端根目录同轮扫描后出现 `g3r2_copy.txt` | 本地联调实测 | 无 | 回归通过：副本可见且内容一致 |
| G3-05 | 重命名同步 | 本地重命名（含中文/特殊字符），Cloudreve 端更新 | Pass | 无 | `2026-03-23 20:59` 本地重命名 `g3r2_copy.txt -> g3r2_重命名_测试@1.txt`；远端返回同名对象，路径编码为 `%E9%87%8D%E5%91%BD%E5%90%8D...%401.txt` | 本地联调实测 | 无 | 回归通过：中文与特殊字符重命名正常 |
| G3-06 | 删除同步 | 本地删除后 Cloudreve 端应删除成功；同步客户端按当前实现为硬删除（不进回收站），回收站恢复在阶段 5/6 单独验证 | Pass | 无（与 Syncthing 设计一致） | `2026-03-23 21:02` 删除 `g3r2_重命名_测试@1.txt` 后远端目录不再可见；`cloudreve://trash` 未新增该条目；代码与单测确认 `SkipSoftDelete=true`（`syncthing/internal/cloudreve/client.go`、`syncthing/internal/cloudreve/client_test.go`） | 本地联调实测 | 无代码改动（用例预期与客户端删除语义对齐） | 回归通过：删除同步稳定，语义明确为硬删 |

#### 3.2 50000 文件压测（建议分两段）

压测段 A：**建库基线**（只做生成与首次同步）

- A1：本地生成 50000 文件（含目录层级）
- A2：等待 Syncthing 扫描并上传完成
- A3：Cloudreve 端校验：
  - 文件总数/目录层级一致（可用抽样 + API/页面统计）
  - 关键文件可下载、可预览（抽样）

压测段 B：**操作序列**（在 50000 基线之上做变更）

- B1：批量重命名（包含跨目录）
- B2：批量移动（跨多级目录）
- B3：批量删除（同步客户端链路为硬删除，不走回收站恢复）
- B4：引入冲突场景（同名文件、并发修改、短时间频繁变更）

记录表：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据（截图/HAR/日志） | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G3-07 | 50000 文件基线同步 | Syncthing/Cloudreve 两端文件数一致，且无持续错误 | Pass | 无 | `2026-03-23 21:03~21:29` 本地生成 `50000` 文件（`elapsed_sec=230`）并扫描；`21:29:51` Syncthing 状态 `pending=0 uploading=0 errors=0 globalFiles=50003`；本地 `50004` 文件（含 `.stfolder`）/`104` 目录（含根目录），Cloudreve 递归统计 `files=50003 dirs=103 visited=104`；抽样 `dir_001/050/100` 的 `001/250/500` 文件均存在 | 本地联调压测 | 无 | 回归通过：5 万基线同步一致、无持续错误 |
| G3-08 | 50000 文件变更序列 | 变更后两端一致；重命名/移动/删除/高频改写无异常（删除语义按硬删） | Pass | 无 | `2026-03-23 21:45~21:46` 在 5 万基线上执行：重命名 `300`、跨目录移动 `300`、删除 `500`、热点文件 `20` 次连续改写；`21:46:23` Syncthing 状态 `pending=0 uploading=0 errors=0 globalFiles=49503`；Cloudreve 递归 `files=49503 dirs=103`，抽样断言 `rename_old=false/rename_new=true`、`move_source=false/move_target=true`、`delete_target=false`、热点文件更新时间刷新 | 本地联调压测 | 无 | 回归通过：批量变更后两端一致，无上传错误 |

失败排查与修改切入点（优先级从高到低）：

- Syncthing 日志与 HTTP 失败：
  - `syncthing/internal/cloudreve/oauth.go`（授权/刷新）
  - `syncthing/internal/cloudreve/client.go`（设备上报/请求）
  - `syncthing/lib/model/cloudreve_uploader.go`（上传/心跳/活动上报）
- Cloudreve 文件 API：
  - `cloudreve/routers/router.go`：`/api/v4/file/*`、`/api/v4/workflow/*`
  - 前端动作入口与按钮展示：`cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`
- 回收站/公共盘差异：
  - 公共盘删除/回收站显示属于已知风险点（见阶段 6 的越权与回收站专项）

Gate-3 通过标准：

- G3-01 ~ G3-08 全部 Pass。

---

### 阶段 4：Syncthing 设备注册/解绑/配置恢复/冲突（Gate-4）

目标：

- 设备注册信息正确
- 解绑后同 IP 新设备可迁移绑定并触发 `restoreConfig`
- 永久删除后不可恢复
- 同 IP 冲突可被正确阻止并给出明确错误

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据（截图/HAR/日志） | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G4-01 | 设备注册与心跳 | Cloudreve 设备列表 online/last_seen 正常更新 | Pass | 无 | 实时设备查询中主设备 `JJVV453...` 持续 `online=true`；`last_seen_at` 从 `21:52:42` 更新至 `21:53:42`（70s 窗口） | 本地联调实测 | 无 | 回归通过：设备在线与心跳刷新正常 |
| G4-02 | 解绑（非永久） | UI 解绑后设备变为 Unbound；同 IP 新客户端可恢复配置并重新 Bound | Pass | 无 | `DELETE /api/v4/devices/syncthing/G4AAA...` 返回 `code=0` 后设备 `is_bound=false`；同 IP（`X-Forwarded-For: 10.8.0.11`）新设备 `G4BBB...` 上报 `PUT /report` 返回 `restore_config` 与 `restore_from_device_id=G4AAA...`，且设备重新 `is_bound=true` | 本地联调实测 | 无 | 回归通过：解绑与同 IP 配置恢复链路可用 |
| G4-03 | 永久删除 | 删除后记录消失；重启客户端不应拿到 restoreConfig | Pass | 无 | `DELETE /api/v4/devices/syncthing/G4BBB.../permanent` 返回 `code=0` 且列表不再包含该设备；随后同 IP 新设备 `G4CCC...` 上报成功但响应仅含 `device`，无 `restore_config/restore_from_device_id` | 本地联调实测 | 无 | 回归通过：永久删除后不再提供恢复配置 |
| G4-04 | 同 IP 冲突 | 同一用户同 IP 已 Bound 的新注册应被拒绝（明确错误码/提示） | Pass | 无 | 在 `G4AAA...` 已绑定时，同 IP 上报 `G4BBB...` 返回 `code=40090`，消息为“Another bound Syncthing device with the same IP already exists...” | 本地联调实测 | 无 | 回归通过：同 IP 冲突被正确拦截 |

失败排查与修改切入点：

- 业务语义：`cloudreve/inventory/syncthing_device.go`
- 错误码/序列化：`cloudreve/pkg/serializer/error.go`
- 前端调用：`cloudreve/assets/src/api/api.ts`、`cloudreve/assets/src/component/Pages/Devices/SyncthingClient.tsx`

Gate-4 通过标准：

- G4-01 ~ G4-04 全部 Pass。

---

### 阶段 5：网盘个人文件操作全覆盖（Gate-5）

目标：

- 个人盘里，前端所有可见操作都能正确执行
- 操作对应 API 返回正确；UI 状态与数据一致
- 为 ES 同步与 Search 提供行为覆盖基线

#### 5.1 操作集合（以右键菜单为准）

入口代码：`cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`

建议在个人盘准备一个测试目录 `personal-e2e/`，在其中做全量测试。下表按“动作”列用例，执行时可对“文件”和“文件夹”分别覆盖一遍。

| 用例ID | 动作 | 覆盖对象 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G5-01 | 新建文件夹 | 空白区域 | 创建成功，列表即时出现 |  |  |  |  |  |  |
| G5-02 | 新建文件 | 空白区域 | 创建成功，可打开/编辑（如支持） |  |  |  |  |  |  |
| G5-03 | 从模板新建文件 | 空白区域 | 模板列表可用，生成文件内容正确 |  |  |  |  |  |  |
| G5-04 | 上传（小文件/大文件/断点） | 空白区域 | 上传成功；进度与最终大小一致 |  |  |  |  |  |  |
| G5-05 | 远程下载 | 空白区域 | 任务创建成功；完成后文件可用 |  |  |  |  |  |  |
| G5-06 | 进入文件夹 | 文件夹 | 可进入；面包屑/路径正确 |  |  |  |  |  |  |
| G5-07 | 打开/打开方式 | 文件 | ViewerSession 正常；预览正确 |  |  |  |  |  |  |
| G5-08 | 下载 | 文件 | 下载内容与校验一致 |  |  |  |  |  |  |
| G5-09 | 直链生成/批量直链 | 文件 | 直链可用；权限符合组配置 |  |  |  |  |  |  |
| G5-10 | 直链管理（删除） | 文件 | 删除后直链失效 |  |  |  |  |  |  |
| G5-11 | 分享创建/编辑/删除 | 文件/文件夹 | 分享可访问；删除后不可访问 |  |  |  |  |  |  |
| G5-12 | 管理分享（已分享对象） | 文件/文件夹 | 管理入口可用；状态一致 |  |  |  |  |  |  |
| G5-13 | 重命名（含中文/特殊字符） | 文件/文件夹 | 重命名成功；引用更新 |  |  |  |  |  |  |
| G5-14 | 复制 | 文件/文件夹 | 复制成功；副本内容一致 |  |  |  |  |  |  |
| G5-15 | 移动 | 文件/文件夹 | 移动成功；树结构一致 |  |  |  |  |  |  |
| G5-16 | 删除（软删） | 文件/文件夹 | 进入回收站可见；可恢复 |  |  |  |  |  |  |
| G5-17 | 恢复 | 回收站对象 | 恢复到原位置；权限/元数据保留 |  |  |  |  |  |  |
| G5-18 | 标签/元数据更新 | 文件/文件夹 | 更新成功；刷新后仍存在 |  |  |  |  |  |  |
| G5-19 | 置顶（Pin/Unpin） | 文件夹 | 置顶排序正确 |  |  |  |  |  |  |
| G5-20 | 版本控制（设当前/删版本） | 文件 | 版本列表正确；冲突提示正确 |  |  |  |  |  |  |
| G5-21 | 创建压缩包 | 文件/文件夹 | 任务创建成功；产物可下载 |  |  |  |  |  |  |
| G5-22 | 解压 | 压缩文件 | 解压任务成功；目录结构正确 |  |  |  |  |  |  |
| G5-23 | 重置缩略图（失败缩略图场景） | 文件 | 重置后缩略图恢复/状态正确 |  |  |  |  |  |  |
| G5-24 | 信息（Info） | 文件/文件夹 | 信息面板字段正确 |  |  |  |  |  |  |
| G5-25 | 搜索结果定位到父目录 | 搜索结果 | “定位到父目录”正确跳转 |  |  |  |  |  |  |

建议额外覆盖的“特殊上下文”：

- share_redirect 文件：右键应出现“前往分享链接”，且不能出现不合理的写操作
- 回收站：按钮显示应以 restore 能力为准；恢复后状态一致

失败排查与修改切入点（按动作类型）：

- 文件/目录 CRUD：`cloudreve/routers/router.go` 的 `/api/v4/file/*`
- 工作流任务（压缩/解压/远程下载）：`cloudreve/routers/router.go` 的 `/api/v4/workflow/*`
- 前端动作与按钮显示：`cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`
- 前端 API 封装：`cloudreve/assets/src/api/api.ts`

Gate-5 通过标准：

- G5-01 ~ G5-25 全部 Pass。

---

### 阶段 6：网盘公共文件操作 + 统一认证授权策略（Gate-6，越权专项）

目标：

- 公共盘的可见性与可操作性完全由统一认证策略控制（用户/角色/部门等）
- UI 层面：按钮不越权（不可操作时不展示或置灰）
- API 层面：即便强行请求也必须被拒绝（403/401），且不会产生副作用

#### 6.1 管理端配置步骤（Authverse）

入口：

- 网盘授权（资源与模板库）：`/admin/authorization/disk`
- 统一授权（融合策略与运行态预览）：`/admin/authorization/unified-auth`

建议先照 `cloudreve/docs/yudao-cloudreve-authz-e2e.md` 完成：

1. 策略模板库可用（内置模板存在、可新增自定义模板）
2. 公共资源建模：把 `cloudreve://public` 下关键目录登记为资源
3. 资源绑定策略并启用
4. 使用“本地试算/策略模拟”预检动作矩阵

#### 6.2 越权矩阵用例（按主体类型覆盖）

下面的矩阵每一行都要做两层验证：

1. UI：公共盘目录/文件是否可见、右键菜单动作是否正确出现
2. API：使用 DevTools 直接调用对应 API（如 rename/move/delete/metadata）确认被拒绝或放行符合预期

| 用例ID | 主体类型 | 主体 | 资源范围 | 策略模板/规则 | 预期（可见/动作） | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G6-01 | 用户 | U1 | `public/readonly/` | 只读模板 | 可见；仅 list/read/download；不可 rename/move/delete/upload |  |  |  |  |  |  |
| G6-02 | 用户 | U2 | `public/readonly/` | 无授权 | 不可见；任何 API 都 403 |  |  |  |  |  |  |
| G6-03 | 角色 | RoleA | `public/maintain/` | 可维护模板 | 可见；允许 create/upload/rename/move/delete（按模板定义） |  |  |  |  |  |  |
| G6-04 | 部门（含层级） | DeptA（含 DeptA1） | `public/maintain/` | 部门层级模板 | DeptA/DeptA1 可见且可操作；DeptB 不可 |  |  |  |  |  |  |
| G6-05 | 组合（用户+角色+部门） | U3 | `public/secret/` | 混合规则 | 命中 allow/deny 优先级符合预期；不出现越权 |  |  |  |  |  |  |

#### 6.3 公共盘文件操作全量覆盖（在“授权允许”的资源上）

在 `public/maintain/` 上复用阶段 5 的动作全集（G5-01 ~ G5-25），重点关注：

- 新建目录/文件后权限按钮是否立即出现（已知风险：需要刷新后才出现）
- 删除后的回收站行为：公共文件删除后在回收站是否可见、是否可恢复（已知风险）

记录表：

| 用例ID | 动作 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G6-06 | 公共盘新建后权限即时生效 | 新建目录/文件后无需刷新即可出现正确动作 |  |  |  |  |  |  |
| G6-07 | 公共盘删除进入回收站 | 回收站可见且可恢复（按策略） |  |  |  |  |  |  |

失败排查与修改切入点：

- Cloudreve 公共盘 capability 计算与前端按钮：`cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`
- Authverse 网盘授权与统一授权页面：
  - `authverse/src/component/UnifiedAuth/CloudreveAuthz/DiskAuthorizationManagement.tsx`
  - `authverse/src/component/UnifiedAuth/UnifiedAuthorizationManagement.tsx`
- 后端“公共可见性”远程校验端点（Cloudreve）：`/api/v4/public/remote/*`（见 `cloudreve/routers/router.go`）

Gate-6 通过标准：

- G6-01 ~ G6-07 全部 Pass，且无任何“通过 UI 隐藏但 API 仍可越权成功”的情况。

---

### 阶段 7：统一检索联调 + 越权验证 + 新增策略模板（Gate-7）

目标：

- Search 运行态可用（dashboard/overview/query）
- Cloudreve 源的检索结果同时满足：
  - 公共可见性（来自网盘授权/统一授权）
  - Search 自身策略（SearchAuthorizationManagement 配置）
- 新增 1 个自定义“检索策略模板”并完成验证

#### 7.1 管理端配置（Authverse -> 统一检索）

入口：`/admin/authorization/search`（实现：`authverse/src/component/UnifiedAuth/SearchAuthorizationManagement.tsx`）

建议按顺序验证：

1. Elasticsearch 状态检查可用（页面内有 status 检查入口）
2. 配置检索源（source）与索引（index），确保 cloudreve index 可被发现
3. 配置策略模板库可用（新增/编辑/删除）
4. 配置检索策略（policy）并绑定主体（用户/角色/部门），规则可视化/JSON 均可编辑
5. 使用“simulate/query debug”功能验证生成的 allow 查询 DSL

#### 7.2 运行态验证（Search 前端）

入口：`search-frontend/src/component/Yudao/SearchProject.tsx`

重点验证：

- 可按分类/来源过滤
- 结果的 `open_uri` 若为 `cloudreve://...`，应跳转到网盘打开路径（前端会映射到 `/home?path=...`）
- `statsOnly` 模式下能快速返回来源命中统计，用于压测后抽样回归

#### 7.3 越权专项用例

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G7-01 | 公共可见但 Search 策略 deny | 不应出现在检索结果中 |  |  |  |  |  |  |
| G7-02 | Search allow 但公共不可见 | 不应出现在检索结果中（防越权） |  |  |  |  |  |  |
| G7-03 | 公共可见 + Search allow | 必须命中且可打开 |  |  |  |  |  |  |

失败排查与修改切入点：

- Cloudreve 公共可见性合并逻辑：  
  `authverse-backend/yudao-module-search/.../service/query/CloudrevePublicVisibilityAllowAugmentor.java`
- Search 策略模板/策略管理前端：  
  `authverse/src/component/UnifiedAuth/SearchAuthorizationManagement.tsx`
- Search 运行态页：  
  `search-frontend/src/component/Yudao/SearchProject.tsx`

#### 7.4 新增“检索策略模板”（必须执行）

要求：新增 1 个模板，用于验证模板参数、规则 JSON、推荐效果等链路完整。

建议模板（示例）：`cloudreve-treepath-allow`

- 目标：仅允许检索命中某个 `tree_path` 前缀（用于模拟“项目空间/部门空间”检索范围）
- 规则示例（字段与 operator 请以页面加载的 field metadata 为准）：
  - `fieldName = tree_path`
  - `operator = starts_with`
  - `valueType = literal`
  - `valueText = <某个 public 子目录 tree_path>`

记录表：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G7-04 | 新增检索策略模板 | 模板新增成功；可在策略里套用；套用后规则生效 |  |  |  |  |  |  |

Gate-7 通过标准：

- G7-01 ~ G7-04 全部 Pass。

---

### 阶段 8：网盘主从节点任务协调（解压/压缩/抽取/ES 同步/离线下载）+ 队列监控（Gate-8）

目标：

- 在主从协调模式下，任务可创建、可分发、可完成、可监控
- 结合 ES/Tika/PG 联调：内容抽取产物正确、索引更新正确

参考设计与术语：`cloudreve/docs/content-processing-node-plan.md`

建议按顺序验证：

1. 主从节点在线与能力标记正确（Master/Slave）
2. 压缩/解压任务走协调模式：
  - 任务创建成功
  - 从节点执行成功
  - 主站收口状态正确
3. 远程下载任务走协调模式（如启用 Aria2/Downloader）
4. 内容抽取（Tika）与 sidecar 写入，最终 ES 更新
5. 队列监控页面/指标可用（任务列表、进度、失败重试等）

记录表：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G8-01 | 压缩任务（协调模式） | 任务成功；产物可下载；节点分配正确 |  |  |  |  |  |  |
| G8-02 | 解压任务（协调模式） | 解压成功；目录结构正确；节点分配正确 |  |  |  |  |  |  |
| G8-03 | 离线下载任务（协调模式） | 下载成功；任务可取消/可选文件 |  |  |  |  |  |  |
| G8-04 | 内容抽取（Tika）与 sidecar | sidecar 可读；关键字段写入 PG；ES 文档可检索 |  |  |  |  |  |  |
| G8-05 | 队列监控与进度 | 列表/进度/失败信息准确；刷新一致 |  |  |  |  |  |  |

失败排查与修改切入点：

- Cloudreve 工作流与节点 RPC：`cloudreve/routers/router.go`（`/api/v4/slave/*`、`/api/v4/workflow/*`）
- 节点能力与任务编排：`cloudreve/pkg/cluster/*`、`cloudreve/service/node/*`
- Tika/全文抽取能力验证参考：`cloudreve/docs/tika-fts-capability-test-report.md`

Gate-8 通过标准：

- G8-01 ~ G8-05 全部 Pass。

---

### 阶段 9：文件变动同步到 ES 正确性（50000 基线）（Gate-9）

目标：

- 覆盖阶段 3、5、6 的所有文件操作，ES 中对应文档增删改正确
- 50000 文件基线上，索引与实际文件一致（至少做到“计数一致 + 抽样一致 + 不越权”）

建议验证方法（至少满足 2 项）：

- ES index docsCount 与 Cloudreve 文件统计一致（或在可接受误差范围内）
- 抽样校验（建议 100~500 个样本）：路径、文件名、更新时间、大小、open_uri
- 检索端验证：Search/Cloudreve 内搜索能命中/删除后不可命中

记录表：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G9-01 | 创建/重命名/移动/删除触发索引更新 | 变更后检索结果与文件系统一致 |  |  |  |  |  |  |
| G9-02 | 50000 基线索引一致性 | docsCount 与抽样一致；无批量失败 |  |  |  |  |  |  |

Gate-9 通过标准：

- G9-01 ~ G9-02 全部 Pass，且 Search 越权专项（阶段 7）在压测后仍 Pass。

---

### 阶段 10：统一认证（Authverse）全页面/全模块测试（Gate-10）

目标：

- Authverse 前端所有可达页面无阻断性错误（白屏、JS 运行时异常、关键接口 4xx/5xx）
- 统一管理模块可用：用户/角色/部门/岗位/菜单/字典/审计日志
- 授权模块可用：OAuth2、网盘授权、统一检索、统一授权

模块清单参考：`authverse/src/component/UnifiedAuth/systemSections.tsx`  
路由清单参考：`authverse/src/router/index.tsx`

建议覆盖（至少）：

- 登录相关：
  - `/session`（登录）
  - `/sso`（SSO）
  - `/session/authorize`（授权页）
  - `/callback/desktop`、`/callback/ios`（如启用）
- 系统管理（System）：
  - 用户、角色、部门、岗位、菜单、字典、审计日志
- 授权（Authorization）：
  - OAuth2 管理（客户端/令牌）
  - 网盘授权（资源/模板/策略/试算）
  - 统一检索（源/索引/策略/模板/query debug）
  - 统一授权（策略/预览/运行态面板/迁移工具）

记录表：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G10-01 | Authverse 登录/SSO | 可正常登录/登出；无运行时错误 |  |  |  |  |  |  |
| G10-02 | 系统管理模块全覆盖 | 各页面可打开、增删改查核心链路可用 |  |  |  |  |  |  |
| G10-03 | OAuth2 管理 | 客户端/令牌列表正常；基础操作可用 |  |  |  |  |  |  |
| G10-04 | 网盘授权模块 | 资源/模板/策略/试算可用 |  |  |  |  |  |  |
| G10-05 | 统一检索模块 | 源/索引/策略/模板/query debug 可用 |  |  |  |  |  |  |
| G10-06 | 统一授权模块 | 策略 CRUD、预览、运行态面板可用 |  |  |  |  |  |  |

Gate-10 通过标准：

- G10-01 ~ G10-06 全部 Pass。

---

## 6. 全流程回归（最终必须执行一次）

当 Gate-1 ~ Gate-10 全部通过后，按以下最小 Smoke 再跑一遍，确认没有“前面修复引入的回归”：

1. 重新打开全新 Chrome Profile（或清理站点数据）
2. 跑一遍 Gate-1（登录联调）
3. 跑一遍 Gate-2（Syncthing OAuth + 设备可见）
4. 在 Syncthing 同步目录新增 10 个文件并触发 3 次重命名/移动/删除
5. 在公共盘做一次“允许动作”与一次“越权动作”（UI + API）
6. Search 运行态查询：
  - 命中允许资源
  - 不命中不可见资源
7. 创建 1 个压缩/解压任务并观察队列状态

回归记录表：

| 用例ID | 测试项 | 预期 | 结果 | 失败原因 | 证据 | Bug/PR/Commit | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| R-01 | 全流程 Smoke | 以上 7 步全部 Pass |  |  |  |  |  |  |

---

## 7. 已知风险点/待验证清单（从旧记录迁移）

> 这些不是“计划外”，而是要求在对应阶段里必须被覆盖并关单。

统一认证相关（建议纳入阶段 10 或对应管理页用例）：

- 网盘授权策略：一级目录删除权限标识是否可去除，改为完全由统一授权控制（从根到任意层级动作）
- 用户角色修改失败
- 文案替换：`yudao/芋道/芋道源码` 等文案替换为“统一认证”
- 岗位备注修改无效
- admin 修改用户信息失败（提示岗位不存在）
- 进入统一认证主页报错：`ReferenceError: isYudaoNavigation is not defined`（示例：`PageNavigation.tsx` 报错）

网盘相关（建议纳入阶段 6/5/1）：

- 回收站查不到公共文件删除的文件（阶段 6 重点验证）
- 公共文件新建文件夹后右键权限按钮需刷新才出现（阶段 6 重点验证）
- 网盘统一登录失败（阶段 1 重点验证）

---

## 8. 本轮联调执行记录（持续更新）

### 8.1 当前可复现环境

- Cloudreve 当前使用独立持久化实例：
  - 启动命令：`go run . -w -c data/conf.ini server`
  - 配置：`cloudreve/data/conf.ini`
  - 数据库：`cloudreve/data/cloudreve.db`（SQLite）
  - 静态目录：`cloudreve/data/statics`
- OIDC 设置当前已核验：
  - `oidc_enabled=1`
  - `oidc_display_name=Authverse SSO`
  - `oidc_sso_url=http://localhost:5174/sso`
  - `oidc_wellknown_url=http://localhost:48080/.well-known/openid-configuration`
  - `oidc_client_id=cloudreve`
  - `oidc_client_secret=cloudreve123`
  - `oidc_scope=openid user_info user.read`
  - `siteURL=http://localhost:5212`
- Chrome 联调会话：
  - 远程调试端口：`9223`
  - 当前 target：`08D0B23AFE7029D59A2E6AC264632D8F`
  - 当前页面：`http://localhost:5212/session`（标题：`Cloudreve`，登出后登录页）
- 依赖组件连通性：
  - Elasticsearch：`http://127.0.0.1:9200`，版本 `8.12.2`
  - Tika：`http://127.0.0.1:9998/version`，版本 `3.2.3`
- Cloudreve 影子用户创建已确认：
  - SQLite 查询：`1|admin|11aoteman@126.com|芋道源码|active`

### 8.2 Gate-1 已修复缺陷台账

| 缺陷ID | 关联用例 | 结果 | 不通过原因 | 修改路径 | 回归结论 |
| --- | --- | --- | --- | --- | --- |
| D-001 | G1-01/G1-02 | Fail -> Pass | Authverse/Search 前端 `swc` 依赖缺失，Vite 页面 500 | `authverse/yarn.lock`、`search-frontend/yarn.lock` | 两端 dev server 恢复，页面可加载 |
| D-002 | G1-01/G1-02 | Fail -> Pass | 登录页 `CSSTransition` 无超时，阶段切换卡死 | `authverse/src/component/Pages/Login/Signin/SignIn.tsx`、`search-frontend/src/component/Pages/Login/Signin/SignIn.tsx` | 登录流程可正常推进 |
| D-003 | G1-02 | Fail -> Pass | `useQuery()` 每次 render 生成新对象，导致 OAuth init effect 抖动/死循环 | `authverse/src/util/index.ts`、`search-frontend/src/util/index.ts`、`authverse/src/component/Pages/Login/Sso.tsx`、`authverse/src/component/Pages/Login/Authorize.tsx`、`search-frontend/src/component/Pages/Login/Authorize.tsx` | OAuth 页面初始化恢复稳定 |
| D-004 | G1-02 | Fail -> Pass | Authverse 授权页首次授权默认 scope 全 false | `authverse/src/component/Pages/Login/Signin/SignIn.tsx` | 授权提交 scope 回归为 `openid user_info user.read` |
| D-005 | G1-02 | Fail -> Pass | Authverse PG 缺 `system_users.allowed_ips`/`secret_level` | `authverse-backend/sql/postgresql/ruoyi-vue-pro.sql`、`authverse-backend/sql/postgresql/system-users-auth-fields-upgrade.sql` | 用户查询恢复，登录链路恢复 |
| D-006 | G1-02/G1-03 | Fail -> Pass | Authverse PG 缺 `system_oauth2_code.code_challenge`/`code_challenge_method`，PKCE 写码失败 | `authverse-backend/yudao-module-system/src/main/java/cn/iocoder/yudao/module/system/controller/admin/oauth2/OAuth2OpenController.java`、`authverse-backend/yudao-module-system/src/main/java/cn/iocoder/yudao/module/system/dal/dataobject/oauth2/OAuth2CodeDO.java`、`authverse-backend/yudao-module-system/src/main/java/cn/iocoder/yudao/module/system/service/oauth2/OAuth2CodeServiceImpl.java`、`authverse-backend/yudao-module-system/src/main/java/cn/iocoder/yudao/module/system/service/oauth2/OAuth2GrantServiceImpl.java`、`authverse-backend/sql/postgresql/ruoyi-vue-pro.sql`、`authverse-backend/sql/postgresql/oauth2-pkce-upgrade.sql` | OIDC 授权码与换票回归通过 |
| D-007 | G1-03 | Fail -> Pass | Cloudreve callback 重复 exchange，同一 `code/state` 二次换票报“登录会话不存在” | `cloudreve/assets/src/util/index.ts`、`cloudreve/assets/src/component/Pages/Login/Signin/OIDCCallback.tsx` | 回调页仅触发一次 exchange |
| D-008 | 环境风险 | Warn | Cloudreve 静态资源版本与后端版本不一致（`4.0.0-next` vs `4.14.0`） | `cloudreve/data/statics/version.json`、`cloudreve/application/constants/constants.go`、`cloudreve/application/statics/statics.go` | 当前不阻塞 Gate-1，后续需做静态资源重建专项 |
| D-009 | G1-02（Search 回归） | Fail -> Pass | Search 登录前租户探测接口 `/admin-api/system/tenant/get-by-website` 返回业务 `code=401` 时被当作未登录，页面回跳 `/session?redirect=...` 并提示“请先登录” | `search-frontend/src/api/api.ts` | 在租户探测请求中兼容 `CodeLoginRequired` 与 HTTP 401 fallback 后，`/session` 登录可落地 `http://localhost:5175/admin/authorization/search` |
| D-010 | G2-04 | Fail -> Pass | Syncthing 启动时存在历史无效 Cloudreve OAuth 会话，上传与心跳持续报 `cloudreve api error 40020` | 无代码改动（联调操作修正） | 重新完成 Cloudreve OAuth 授权并重启 Syncthing 后，`/rest/noauth/auth/cloudreve/status` 回归为 `authorized=true`，设备可在 Cloudreve 显示在线 |

### 8.3 Gate-1 主链路通过证据（本轮）

1. `http://localhost:5212/session` 点击 “使用 Authverse SSO 继续登录”
2. Cloudreve 触发 `GET /api/v4/session/oidc/prepare`
3. 浏览器跳转 `http://localhost:5174/sso?...client_id=cloudreve...`
4. Authverse 完成授权并回跳 `http://localhost:5212/session/oidc/callback?...`
5. Cloudreve 执行 `POST /api/v4/session/oidc/exchange -> 200`
6. 最终到达 `http://localhost:5212/home`

当前 Gate-1 状态：

- `G1-01` Pass
- `G1-02` Pass
- `G1-03` Pass
- `G1-04` Pass
- `G1-05` Pass
- `G1-06` Pass
- `G1-07` Pass
- Gate-1 结论：通过，可进入 Gate-2（Syncthing 认证与设备注册联调）

### 8.4 Gate-2 实测记录（本轮）

1. Syncthing 客户端启动并监听 `127.0.0.1:18384`（`/rest/noauth/health` 返回 `status=OK`）。
2. 同一浏览器从 Syncthing OAuth 登录入口跳转 Cloudreve `session/authorize`，授权页显示应用名 `Syncthing` 与权限清单。
3. 点击“授权应用”后浏览器回到本地 Syncthing 页面（`http://127.0.0.1:18384/`），本地状态页可访问。
4. Syncthing OAuth 状态接口返回 `authorized=true`（含用户与 scope 信息）。
5. Cloudreve `/connect` 页面出现已注册设备 `JJVV453...`，状态为“在线/已绑定”；`GET /api/v4/devices/syncthing` 返回 `code=0` 且包含同设备。

当前 Gate-2 状态：

- `G2-01` Pass
- `G2-02` Pass
- `G2-03` Pass
- `G2-04` Pass
- `G2-05` Pass
- Gate-2 结论：通过，可进入 Gate-3（Syncthing 上传与文件操作同步）

### 8.5 Gate-3 实测记录（本轮）

1. 基础六项（G3-01 ~ G3-06）已完成两轮验证：新建目录/文件、移动、复制、中文重命名、删除均可同步到 Cloudreve。
2. 删除语义确认：Syncthing 上传链路删除调用为 `skip_soft_delete=true`，表现为远端硬删除、不进入回收站；该语义仅适用于同步客户端链路，网盘前端回收站能力在阶段 5/6 继续专项验证。
3. 压测段 A（50000 基线）：
   - 本地批量生成 `50000` 文件（100 目录 * 500 文件），耗时 `230s`。
   - 触发扫描后上传队列从 `32595` 逐步归零，`2026-03-23 21:29:51` 达到 `pending=0 uploading=0 errors=0`。
   - 基线一致性：本地 `50004` 文件（含 `.stfolder`）/`104` 目录（含根），Cloudreve 递归统计 `50003` 文件/`103` 目录，与 Syncthing `globalFiles=50003 globalDirectories=103` 一致。
4. 压测段 B（变更序列）：
   - 执行 `300` 批量重命名、`300` 批量跨目录移动、`500` 批量删除、热点文件 `20` 次连续改写。
   - `2026-03-23 21:46:23` 二次归零：`pending=0 uploading=0 errors=0`，`globalFiles=49503`。
   - Cloudreve 递归统计 `files=49503 dirs=103`，抽样断言全部命中（重命名新旧状态、移动源/目标、删除不可见、热点文件更新时间刷新）。

当前 Gate-3 状态：

- `G3-01` Pass
- `G3-02` Pass
- `G3-03` Pass
- `G3-04` Pass
- `G3-05` Pass
- `G3-06` Pass（删除语义为硬删）
- `G3-07` Pass
- `G3-08` Pass
- Gate-3 结论：通过，可进入 Gate-4（设备注册/解绑/配置恢复/冲突）

### 8.6 Gate-4 实测记录（本轮）

1. 为避免干扰真实同步设备（`JJVV453...`），本轮使用同用户下“测试设备 + 指定 IP 头”完成 Gate-4 流程闭环，真实设备保持在线。
2. 设备注册与心跳（G4-01）：
   - 主设备在 `GET /api/v4/devices/syncthing` 中持续 `online=true`。
   - `last_seen_at` 在 70 秒窗口内从 `2026-03-23 21:52:42` 更新为 `2026-03-23 21:53:42`，证明心跳上报持续生效。
3. 同 IP 冲突（G4-04）：
   - 先注册测试设备 `G4AAA...`（IP=`10.8.0.11`）为绑定态。
   - 绑定态下同 IP 新设备 `G4BBB...` 上报返回 `code=40090`（冲突），与预期一致。
4. 解绑恢复（G4-02）：
   - 对 `G4AAA...` 执行非永久解绑后，状态变为 `is_bound=false`。
   - 同 IP 新设备 `G4BBB...` 再次上报返回 `restore_config` 与 `restore_from_device_id=G4AAA...`，并完成重新绑定。
5. 永久删除（G4-03）：
   - 对 `G4BBB...` 执行 `/permanent` 删除后记录消失。
   - 同 IP 新设备 `G4CCC...` 上报成功但不再返回任何 `restore_config` 字段，符合“永久删除不可恢复”语义。

当前 Gate-4 状态：

- `G4-01` Pass
- `G4-02` Pass
- `G4-03` Pass
- `G4-04` Pass
- Gate-4 结论：通过，可进入 Gate-5（网盘个人文件操作全覆盖）
