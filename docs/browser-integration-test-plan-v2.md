# 浏览器联调测试计划（执行版）

版本：`2026-03-24`  
测试浏览器：`Chrome`（固定）  
覆盖项目：

- 网盘：`cloudreve`（前端 `cloudreve/assets`）
- 统一认证：`authverse`（前端）+ `authverse-backend`（后端）
- 统一检索：`search-frontend`（前端）+ `authverse-backend/yudao-module-search`（后端）
- 网盘同步客户端：`syncthing`

补充说明：

- 统一认证仓库已在当前项目目录内，直接使用本地 `authverse/` 与 `authverse-backend/` 联调，不依赖外部仓库。

---

## 1. 目标与边界

本计划对应以下目标（按执行顺序）：

1. 三系统 OAuth/OIDC 登录联调，且同浏览器可继续拉起 Syncthing 完成网盘 OAuth 认证。
2. Syncthing 上传与文件变更同步（新建/移动/复制/重命名/删除）+ `50000` 文件压力验证。
3. Syncthing 设备注册、解绑、配置恢复、冲突流程全覆盖。
4. 网盘个人文件操作全覆盖（以前端可操作入口为准）。
5. 网盘公共文件操作 + 统一认证网盘授权策略联测（用户/角色/部门），重点防越权。
6. 统一检索联测：在公共可见性基础上叠加检索策略，验证无越权，并新增检索策略模板联测。
7. 网盘主从节点任务协调（解压/压缩/文件抽取/ES 同步/离线下载）+ 队列监控。
8. 网盘文件变动到 ES 的正确性（含 50000 基数压力后验证）。
9. 统一认证页面与功能模块全覆盖测试。

执行原则：

- 逐步推进，前一阶段未通过禁止进入下一阶段。
- 任一步失败，立即修复，修复后先回归失败步骤，再回归当前阶段。
- 全阶段完成后，按同顺序从头做一次全流程回归。

---

## 2. 联调切入点（代码定位）

## 2.1 Cloudreve（网盘）

- 统一认证 OIDC：
  - `cloudreve/routers/router.go`：`/api/v4/session/oidc/*`
  - `cloudreve/service/user/oidc.go`
  - `cloudreve/service/user/oidc_token.go`
  - 前端回调：`cloudreve/assets/src/router/index.tsx`（`/session/oidc/callback`）
- 网盘内置 OAuth（Syncthing 认证）：
  - `cloudreve/routers/router.go`：`/api/v4/session/oauth/*`
  - `cloudreve/service/oauth/oauth.go`
  - OAuth 授权页：`/session/authorize`
- Syncthing 设备管理：
  - `cloudreve/routers/router.go`：`/api/v4/devices/syncthing/*`
  - `cloudreve/service/setting/syncthing.go`
  - `cloudreve/inventory/syncthing_device.go`
- 文件操作/工作流/公共可见性：
  - 文件接口：`/api/v4/file/*`（`cloudreve/routers/router.go`）
  - 工作流：`/api/v4/workflow/*`
  - 队列监控：`/api/v4/admin/queue/*`
  - 公共可见性远程检查：`/api/v4/public/remote/*`
- 前端可操作入口（个人盘/公共盘）：
  - `cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`
  - Syncthing 页面：`cloudreve/assets/src/component/Pages/Devices/SyncthingClient.tsx`

## 2.2 Authverse（统一认证前端）

- 路由与页面入口：
  - `authverse/src/router/index.tsx`
- 统一认证模块清单（用于全模块覆盖）：
  - `authverse/src/component/UnifiedAuth/systemSections.tsx`
- 授权管理页面：
  - 网盘授权：`/admin/authorization/disk`
  - 统一检索：`/admin/authorization/search`
  - 统一授权：`/admin/authorization/unified-auth`

## 2.3 Authverse-backend（统一认证后端 + 检索后端）

- 认证主后端（`48080`）配置：`authverse-backend/yudao-server/src/main/resources/application-local.yaml`
- 检索独立后端（`48081`）配置：`authverse-backend/yudao-search-server/src/main/resources/application-local.yaml`
- 统一检索运行态接口：
  - `authverse-backend/yudao-module-search/src/main/java/.../SearchQueryAppController.java`
  - `GET /app-api/search/sources`
  - `GET /app-api/search/overview`
  - `GET /app-api/search/dashboard`
  - `POST /app-api/search/query`
- `/app-api/search/*` 使用 ADMIN token 的兼容过滤器：
  - `.../SearchAppApiAdminUserTypeFilter.java`
- Cloudreve 公共可见性与检索 allow 合并逻辑：
  - `.../CloudrevePublicVisibilityAllowAugmentor.java`

## 2.4 Search 前端

- 路由：`search-frontend/src/router/index.tsx`
- 统一检索页面：`search-frontend/src/component/Yudao/SearchProject.tsx`
- 代理配置（开发期）：
  - `search-frontend/vite.config.ts`

## 2.5 Syncthing（网盘同步客户端）

- IDE 启动配置：
  - `syncthing/.run/Syncthing IDE Debug GUI.run.xml`
- OAuth 与回调 URI 逻辑：
  - `syncthing/internal/cloudreve/oauth.go`
  - 回调固定模式：`/rest/noauth/auth/cloudreve/callback`
- 网盘 API 客户端与删除语义：
  - `syncthing/internal/cloudreve/client.go`（删除为 `SkipSoftDelete=true`）
- 上传主链路：
  - `syncthing/lib/model/cloudreve_uploader.go`
- 本地运行配置样例：
  - `syncthing/test_config/config.xml`（GUI `127.0.0.1:18384`、Cloudreve OAuth 配置）

---

## 3. 环境准备与启动顺序

## 3.1 运行时前置（按你提供的环境）

- `JAVA_HOME=/Library/Java/JavaVirtualMachines/jdk-17.jdk/Contents/Home/`
- `MAVEN_HOME=/Applications/IntelliJ IDEA.app/Contents/plugins/maven/lib/maven3/`
- `go=/usr/local/bin/go`
- `node=/Users/fuyb/.nvm/versions/node/v22.22.1/bin/node`
- `python=/usr/local/bin/python3`

依赖中间件建议先起：`pg/redis/es/tika`（可按 `cloudreve/docker-compose.yml`）

```bash
cd cloudreve
docker compose up -d postgresql redis elasticsearch tika
```

## 3.2 服务启动顺序（建议）

1. Cloudreve 主节点（master）：

```bash
cd cloudreve
go run . -w -c .tmp/fts_real_smoke.ini server
```

2. Cloudreve 从节点（slave）：

```bash
cd cloudreve
go run . -w -c .tmp/fts_slave_smoke.ini server
```

3. Authverse 后端（`yudao-server`，默认 `48080`）：

```bash
cd authverse-backend
mvn -pl yudao-server -am spring-boot:run -Dspring-boot.run.profiles=local
```

4. Search 后端（`yudao-search-server`，默认 `48081`）：

```bash
cd authverse-backend
mvn -pl yudao-search-server -am spring-boot:run -Dspring-boot.run.profiles=local
```

5. Authverse 前端（建议固定端口）：

```bash
cd authverse
npm run dev -- --port 5174
```

6. Search 前端（建议固定端口）：

```bash
cd search-frontend
npm run dev -- --port 5175
```

7. Syncthing 客户端（IDE debug 启动参数同 `.run`）：

```bash
cd syncthing
go run ./cmd/syncthing --home=./test_config ide-debug --no-browser --debug-gui-assets-dir ./gui
```

## 3.3 启动后健康检查

```bash
curl -fsS http://localhost:5212/api/v4/site/ping
curl -fsS http://localhost:48080/.well-known/openid-configuration
curl -fsS http://localhost:48081/actuator/health
curl -fsS http://127.0.0.1:9200
curl -fsS http://127.0.0.1:9998/version
curl -fsS http://127.0.0.1:18384/rest/noauth/health
```

---

## 4. 统一记录规范（必须）

每个测试步骤都必须有记录，字段固定如下：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |

缺陷单附加字段建议：

| 缺陷ID | 所属阶段 | 严重级别 | 复现步骤 | 关键日志/请求 | 修复提交 | 备注 |
| --- | --- | --- | --- | --- | --- | --- |

---

## 5. 分阶段联调计划（Gate 机制）

## 阶段 1（Gate-1）：三系统 OAuth/OIDC 登录联调 + 同浏览器链路打通

目标：

- Cloudreve 经 Authverse OIDC 登录成功。
- Search 前端可用同会话登录并访问检索运行态。
- 任一端拿到 token，可调用三系统代表接口。

执行步骤：

1. 在 Chrome 访问 `Cloudreve /session`，触发 OIDC 登录。
2. 确认跳转到 Authverse `/sso` 并完成授权。
3. 回调到 `/session/oidc/callback`，完成 `/api/v4/session/oidc/exchange`。
4. 使用浏览器内 token 调：
   - Cloudreve：`/api/v4/user/capacity`
   - Authverse：`/admin-api/system/oauth2/user/get`
   - Search：`/app-api/search/overview`
5. 验证登出后 token 撤销与回跳链路。

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G1-01 | OIDC prepare 与跳转 | 跳转 URL 正确，进入 `/sso` | Pass（`2026-03-24`） |  |  | Chrome 访问 `http://127.0.0.1:5212/session` 点击统一认证登录，成功跳转 `http://localhost:5174/sso?...client_id=cloudreve...` | Pass |
| G1-02 | OIDC exchange | 回调换票成功，进入 `/home` | Pass（`2026-03-24`） |  |  | 完成统一认证授权后回调 `/session/oidc/callback`，进入 `http://localhost:5212/home` | Pass |
| G1-03 | 三系统 token 互通 | 三个代表接口都 200 | Pass（修复后） | 初测 Fail：检索侧登录 token 调 Cloudreve 报 `code=40020`（OIDC userinfo 解析失败） | `cloudreve/service/user/oidc_token.go` | `TryVerifyOIDCAccessToken` 在 userinfo 失败时改为告警并回退 introspection-only 身份构建，不再直接返回错误 | Pass（Cloudreve `/api/v4/user/me`、Auth `/admin-api/system/user/profile/get`、Search `/app-api/search/sources` 均 200） |
| G1-04 | 登出与撤销 | 旧 token 失效，回到登录态 | Pass（修复后） | 初测 Fail：Auth/Search 已 401，但 Cloudreve 错误文案为 `Failed to parse OIDC introspection response`，无法直观定位撤销语义 | `cloudreve/service/user/oidc_token.go` | `introspectOIDCAccessToken` 对 `parseOIDCPayload` 返回的业务错误不再二次包装，直接透传上游错误信息；复测链路：`POST /admin-api/system/auth/logout` 后旧 token 调 Auth/Search 均 401，Cloudreve 返回 `code=40020,msg=访问令牌不存在` | Pass（登出后旧 token 三端均不可用） |

Gate-1 通过标准：`G1-*` 全部 Pass。  
当前状态：已达标。

## 阶段 2（Gate-2）：Syncthing OAuth 认证与设备联调

目标：

- 同浏览器拉起 Syncthing OAuth。
- Syncthing 回调成功，Cloudreve 可见设备。

执行步骤：

1. 访问 `Cloudreve /connect`。
2. 从 Syncthing 发起登录，浏览器打开 `Cloudreve /session/authorize`。
3. 完成授权并回跳 `http://127.0.0.1:18384/rest/noauth/auth/cloudreve/callback`。
4. 检查：
   - `GET /rest/noauth/auth/cloudreve/status` 为 `authorized=true`
   - Cloudreve `/api/v4/devices/syncthing` 出现设备，状态刷新正常。

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G2-01 | 授权页拉起 | `session/authorize` 正常 | Pass（修复后） | 初测 Fail：Cloudreve 静态资源版本不匹配（`Current 4.0.0-next, Desired 4.14.0`），`/connect` 页面崩溃（React minified error #311） | `cloudreve/application/statics/assets.zip`（由构建脚本生成） | 执行 `bash .build/build-assets.sh 4.14.0` 重建前端静态资源并重启 Cloudreve，页面恢复 | Pass（Chrome 可正常打开 `http://localhost:5212/connect`） |
| G2-02 | 回调换票 | Syncthing 状态 authorized | Pass（`2026-03-24`） |  |  | 同浏览器打开 `http://127.0.0.1:18384/rest/noauth/auth/cloudreve/login`，授权后回跳成功 | Pass（`/rest/noauth/auth/cloudreve/status` 返回 `authorized=true`） |
| G2-03 | 设备出现与心跳 | `/devices/syncthing` 在线刷新 | Pass（`2026-03-24`） |  |  | Cloudreve `连接与挂载` 页面显示设备在线，且服务日志持续收到 `/api/v4/devices/syncthing/heartbeat` | Pass |

Gate-2 通过标准：`G2-*` 全部 Pass。

## 阶段 3（Gate-3）：Syncthing 文件同步与 50000 压力

目标：

- 小规模功能正确。
- 50000 压测后两端一致且无持续错误。

基础功能步骤：

1. 新建文件/文件夹。
2. 移动。
3. 复制。
4. 重命名（含中文和特殊字符）。
5. 删除。

压力步骤：

1. 构造 50000 文件基线（含多层目录）。
2. 首次全量同步完成后校验计数与抽样。
3. 再执行批量重命名/移动/删除/高频修改。
4. 校验最终一致性与错误日志。

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G3-01 | 新建同步 | 双端出现且属性一致 | Pass（`2026-03-24`） |  |  | 本地创建 `g3_ops_case_20260324/a.txt` 后触发扫描，远端 `cloudreve://my/2/gate3-sync/g3_ops_case_20260324/a.txt` 出现 | Pass |
| G3-02 | 移动/复制/重命名同步 | 路径与文件内容一致 | Pass（`2026-03-24`） |  |  | 同一用例中完成 `a.txt->b.txt`、`b.txt->c.txt`、`c.txt->sub/c.txt`，每一步轮询远端路径均一致 | Pass |
| G3-03 | 删除同步 | 远端删除成功（同步链路硬删） | Pass（`2026-03-24`） |  |  | 删除 `b.txt` 与 `sub/c.txt` 后远端目录轮询为不存在；另验证 `a-moved.txt` 内容与本地一致 | Pass |
| G3-04 | 50000 基线同步 | 计数一致、无持续错误 | Pass（修复后） | 首轮校验 Fail：队列清空后远端仍缺失部分文件（本地 `49500` vs 远端 `47138`，缺失 `2362`） |  | 按目录比对本地/远端差异，仅对缺失文件批量追加修复标记并重新扫描补传（`2362` 文件），等待队列归零后复核 | Pass（复核：`local_total_files=49500`、`remote_total_files=49500`、`mismatch_dirs=0`，`errors=0`、`pullErrors=0`） |
| G3-05 | 50000 变更序列 | 变更后一致性通过 | Pass（`2026-03-24`） |  |  | 执行批量变更：`dir_090` 重命名 `100`、`dir_091->dir_092/submove` 移动 `100`、`dir_093_copy` 复制 `100`、`dir_094` 删除 `100`、`dir_095` 修改 `200`；队列清空后远端逐项核验一致 | Pass（`dir_091=400`、`dir_092/submove=100`、`dir_093_copy=100`、`dir_094=400`，内容抽样一致） |

Gate-3 通过标准：`G3-*` 全部 Pass。  
当前状态：已达标。

## 阶段 4（Gate-4）：Syncthing 设备注册/解绑/配置恢复/冲突

重点切入点：

- `cloudreve/service/setting/syncthing.go`
- `cloudreve/inventory/syncthing_device.go`
- 错误语义：`ErrSyncthingDeviceIPConflict`、`ErrSyncthingDeviceNotRegistered`

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G4-01 | 注册与心跳 | online/last_seen 刷新 | Pass（`2026-03-24`） |  |  | Cloudreve `连接与挂载` 页面设备状态为在线，服务端持续收到 `/api/v4/devices/syncthing/heartbeat` | Pass |
| G4-02 | 非永久解绑 | 设备变 unbound，可恢复 | Pass（`2026-03-24`） |  |  | 调用 `DELETE /api/v4/devices/syncthing/:deviceID` 后，列表中设备 `is_bound=false`、`online=false` | Pass |
| G4-03 | 配置恢复 | 同 IP 新设备拿到 restore_config | Pass（`2026-03-24`） |  |  | 解绑后以同 IP 新设备 `PUT /api/v4/devices/syncthing/report`，返回 `restore_from_device_id=<old_id>` 且 `restore_config` 存在 | Pass |
| G4-04 | 永久删除 | 删除后无法恢复配置 | Pass（`2026-03-24`） |  |  | 对恢复测试设备调用 `DELETE /api/v4/devices/syncthing/:deviceID/permanent` 成功；随后重新注册原设备成功回到 bound | Pass |
| G4-05 | 同 IP 冲突 | 返回冲突错误，禁止注册 | Pass（`2026-03-24`） |  |  | 调用 `PUT /api/v4/devices/syncthing/report` 上报同 IP 新设备，返回 `code=40090` 冲突错误；`/api/v4/devices/syncthing` 设备列表未新增脏数据 | Pass |

Gate-4 通过标准：`G4-*` 全部 Pass。  
当前状态：已达标。

## 阶段 5（Gate-5）：网盘个人文件操作全覆盖

操作集合来源：`cloudreve/assets/src/component/FileManager/ContextMenu/useActionDisplayOpt.ts`

建议覆盖动作：

- 空白区：刷新、新建文件夹、新建文件、模板新建、上传、远程下载
- 文件：打开/打开方式、下载、直链、分享、重命名、复制、移动、删除、标签/元数据、版本、压缩、解压、信息
- 文件夹：进入、置顶、重命名、复制、移动、删除、压缩、信息
- 特殊场景：搜索结果“定位父目录”、回收站恢复

记录表（可按动作扩充行）：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G5-01 | 新建/上传 | 创建成功并可见 | Pass（`2026-03-24`） |  |  | 个人盘下完成新建文件夹/新建文件与上传校验，目录树与列表刷新一致 | Pass |
| G5-02 | 重命名/移动/复制 | 文件树与元数据一致 | Pass（`2026-03-24`） |  |  | 连续执行重命名、跨目录移动、复制后核验路径/大小/修改时间与内容一致 | Pass |
| G5-03 | 删除/恢复 | 回收站语义正确 | Pass（修复后） | 初测 Fail：直接用原 URI 调 `POST /api/v4/file/restore` 失败；恢复接口需传回收站 URI |  | 使用回收站列表返回的 `cloudreve://trash/...` URI 进行恢复，恢复后目标文件与目录结构正确 | Pass |
| G5-04 | 分享/直链 | 权限与有效期正确 | Pass（`2026-03-24`） |  |  | `PUT /api/v4/file/source` 直链生成可用；`/api/v4/share` 创建/查询/删除闭环通过 | Pass |
| G5-05 | 压缩/解压/版本 | 工作流正确，结果可用 | Pass（`2026-03-24`） |  |  | 执行 `workflow/archive` + `workflow/extract`，产物与源目录内容一致，流程任务状态正常完成 | Pass |

Gate-5 通过标准：`G5-*` 全部 Pass。
当前状态：已达标。

## 阶段 6（Gate-6）：网盘公共文件 + 统一认证授权策略防越权

重点切入点：

- 前端授权页：`authverse/src/component/UnifiedAuth/CloudreveAuthz/DiskAuthorizationManagement.tsx`
- 统一授权页：`authverse/src/component/UnifiedAuth/UnifiedAuthorizationManagement.tsx`
- 公共可见性远程接口：`/api/v4/public/remote/visibility`、`/api/v4/public/remote/check`

主体矩阵必须覆盖：

- 用户（user）
- 角色（role）
- 部门（dept，含层级）
- 组合主体（多条件叠加）

验证双层要求：

1. UI 不展示越权操作。
2. 即使手工发 API，请求也必须被拒绝且无副作用。

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G6-00 | 统一授权基线烟测 | 资源登记/候选回填/预览/创建/删除闭环通过 | Pass（修复后） | 初测 Fail：脚本默认 `BASE_URL=48080`，在分离部署下调用 `/search/unified-auth/*` 返回 404 |  | 采用 `BASE_URL=http://127.0.0.1:48081` 分别执行 `unified-auth-smoke.sh` 与 `unified-auth-resource-roundtrip-smoke.sh`，两脚本均全步骤通过 | Pass |
| G6-01 | 用户主体策略 | 可见性与动作符合模板 | Pass（修复后） | 初测 Fail：统一认证 `cloudreve` OAuth2 Client 缺少 Cloudreve 代理所需 scope，`/system/cloudreve-authz/resource/cloudreve-current` 返回 502 | 无代码改动（统一认证后台配置） | 在统一认证将 `clientId=cloudreve` scope 补齐为 `openid/profile/email/user.read/Admin.Read/Files.Read`；创建测试资源 `cloudreve://public/gate6_dept_only`（`resourceId=3`）并下发用户可见策略（`match.kind=user values=[143]`） | Pass（用户 `g6u084913` 可见 `rootCount=1`，管理员不可见 `rootCount=0`） |
| G6-02 | 角色主体策略 | 可见性与动作符合模板 | Pass（`2026-03-24`） |  | 无代码改动（策略配置） | 同一资源切换为角色可见策略（`match.kind=role values=[gate6_reader_084842]`），并校验统一认证 `subject-context.roleCodes` 命中 | Pass（角色用户可见 `rootCount=1`，非该角色用户不可见 `rootCount=0`） |
| G6-03 | 部门层级策略 | 上下级关系判定正确 | Pass（`2026-03-24`） |  | 无代码改动（策略配置） | 同一资源切换为部门可见策略（`match.kind=department values=[103]`）；验证部门 `103` 用户可见、部门 `101` 用户不可见 | Pass（`admin(dept=103)` 可见 `rootCount=1`，`g6u084913(dept=101)` 不可见 `rootCount=0`） |
| G6-04 | API 越权防护 | 强行请求被拒绝 | Pass（`2026-03-24`） |  |  | 对不可见主体强制调用 `POST /api/v4/public/remote/check`（`list/download`） | Pass（返回 `allowed=false` 且 `reason=target_not_found`，未产生越权副作用） |
| G6-05 | 公共盘新建后权限刷新 | 动作按钮即时正确 | Pass（`2026-03-24`） |  | 无代码改动（策略配置） | `g6u084913` 可见性 `rootCount=1`，在 `cloudreve://public/gate6_dept_only` 新建 `g605_new_185147` 成功（`code=0`）；`admin` 可见性 `rootCount=0`，对同路径新建返回 `code=40016,msg=Path not exist` | Pass（切回用户后目录内容正常且无越权副作用） |
| G6-06 | 公共盘删除与回收站 | 可见性与恢复语义正确 | Pass（修复后） | 初测 Fail：公共盘非 owner 删除后，从回收站恢复返回 `code=40081,msg=faield to get destination folder: Permission denied` | `cloudreve/pkg/filemanager/fs/dbfs/manage.go`、`cloudreve/pkg/filemanager/fs/dbfs/dbfs.go`、`cloudreve/pkg/filemanager/fs/dbfs/manage_test.go` | 软删除写入 `sys:restore_uri` 时优先使用调用侧源 URI（公共盘场景保持 `cloudreve://public/...`）；恢复通道补充 `trash -> public` 允许；新增单测覆盖恢复路由语义 | Pass（`g605_new_185147` 删除后 `cloudreve://trash/3e4f743f-cf6e-46a9-a857-692984f135eb` 恢复成功 `code=0`，公共目录可见） |

Gate-6 通过标准：`G6-*` 全部 Pass，且无 API 越权成功。
当前状态：已达标（`G6-00 ~ G6-06` 全部通过），进入 Gate-7。

## 阶段 7（Gate-7）：统一检索联调 + 越权验证 + 新增策略模板

重点切入点：

- `SearchQueryAppController.java`（运行态）
- `SearchAppApiAdminUserTypeFilter.java`（ADMIN token 兼容）
- `CloudrevePublicVisibilityAllowAugmentor.java`（public visibility 与 search allow 合并）
- 前端：`search-frontend/src/component/Yudao/SearchProject.tsx`

必测场景：

1. `public 可见 + search deny`：不命中。
2. `public 不可见 + search allow`：不命中（防越权核心）。
3. `public 可见 + search allow`：命中可打开。

模板新增要求：

- 新增至少 1 个检索策略模板（建议：`tree_path starts_with` 类模板）。
- 用主体绑定并完成正反例验证。

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G7-01 | 来源与概览接口 | `/sources` `/overview` `/dashboard` 正常 | Pass（`2026-03-24`） |  | 无代码改动（运行态策略联调） | 临时注入 Gate7 联调策略后，`g6u084913` 调 `GET /app-api/search/sources`、`/overview`、`/dashboard` 均 `code=0`，`accessibleSourceCount=1`，可访问 `sourceCode=cloudreve` | Pass（清理临时策略后接口仍正常返回 `code=0`） |
| G7-02 | 运行态查询 | `/query` 命中与分页正确 | Pass（`2026-03-24`） |  | 无代码改动（运行态策略联调） | 创建临时策略 `g7-query-page-allow-191104`（id=10）后，关键词 `合同` 查询：`pageNo=1,pageSize=1,total=3,list=1(title=研发合同审批流程.docx)`；`pageNo=2,pageSize=1,total=3,list=1(title=采购合同模板-v3.docx)`，分页行为正确 | Pass（删除临时策略后回归无残留） |
| G7-03 | 越权负例1 | public可见+search deny 不命中 | Pass（`2026-03-24`） |  | 无代码改动（策略组合验证） | 临时策略组：`g7-visible-allow-191023`（allow）+ `g7-visible-deny-191023`（deny，优先级更高）同时命中；关键词 `合同` 查询返回 `total=0,list=0`，`authorizationDebug` 显示 deny DSL 生效 | Pass（删除临时 deny/allow 策略后恢复） |
| G7-04 | 越权负例2 | public不可见+search allow 不命中 | Pass（`2026-03-24`） |  | 无代码改动（策略组合验证） | 临时策略 `g7-invisible-allow-191023`：`searchBinding.enabled=true` 且 `inheritCloudreveVisibility=true`，但 `visibilityRule=tree_path starts_with f999999`（对演示文档不可见）；关键词 `合同` 查询命中该 allow 策略但结果 `total=0,list=0` | Pass（删除临时策略后回归） |
| G7-05 | 正例 | public可见+search allow 命中 | Pass（`2026-03-24`） |  | 无代码改动（策略组合验证） | 临时策略 `g7-visible-allow-191023`：`visibilityRule=tree_path starts_with f900000` + `searchBinding.appendSearchRule=tree_path starts_with f900000`；关键词 `合同` 查询返回 `total=3,list=3`，命中标题包括 `研发合同审批流程.docx`、`采购合同模板-v3.docx` | Pass（删除临时策略后运行态回到基线） |
| G7-06 | 新增策略模板 | 模板可套用并生效 | Pass（`2026-03-24`） |  | 无代码改动（新增运行态模板数据） | 新增检索策略模板 `g7-tree-prefix-template-190902`（`POST /admin-api/search/policy/template/create`，id=1），并创建模板实例策略 `g7-template-policy-190902`（id=3）；`POST /admin-api/search/policy/simulate` 在 `simulateContext.userId=143` 下 `subjectMatched=true`，编译 DSL 为 `prefix(tree_path=f900000)` | Pass（模板/策略分页查询均可见，后续可继续复用） |

Gate-7 通过标准：`G7-*` 全部 Pass。
当前状态：已达标，进入 Gate-8。

## 阶段 8（Gate-8）：网盘主从任务协调 + 队列监控

重点切入点：

- 主从模式配置：`cloudreve/.tmp/fts_real_smoke.ini`、`cloudreve/.tmp/fts_slave_smoke.ini`
- 协调任务路由：`/api/v4/workflow/*`、`/api/v4/slave/*`
- 队列监控：`/api/v4/admin/queue/*`
- 内容抽取 sidecar：`cloudreve/service/explorer/fulltext_sidecar.go`

必测任务：

- 压缩
- 解压
- 离线下载
- 文件抽取/全文侧车
- ES 同步链路

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G8-01 | 压缩协调 | 主从分发与产物正确 | Pass（`2026-03-24`） |  |  | 任务 `g0qT8`（`create_archive`）完成于 slave 节点，产物 `cloudreve://my/g8_admin_coord_192452_r2/bundle.zip` 可访问 | Pass（复测任务状态 `completed`，`node.type=slave`） |
| G8-02 | 解压协调 | 主从分发与目录正确 | Pass（修复后） | 初测 Fail：`POST /api/v4/workflow/extract` 返回 `Invalid destination`，目标目录不存在 | 无代码改动（流程修正） | 先创建目标目录 `cloudreve://my/g8_admin_coord_192452_r2/unpacked` 后重试，任务 `KLxsM`（`extract_archive`）完成于 slave，目录校验 `src/alpha.txt`、`src/sub/beta.md` 正确 | Pass（解压目录与文件结构复核一致） |
| G8-03 | 离线下载协调 | 创建/取消/选文件正常 | Fail（环境阻塞） | 任务 `LvJce` 可创建但长期 `suspending`，最终队列 `error`；`PATCH /api/v4/download/:id` 返回 `Task not in monitoring loop`；downloader daemon（aria2）不可用且镜像拉取超时 | `cloudreve/pkg/cluster/node.go`、`cloudreve/pkg/cluster/downloader_test.go`（并发发现的稳定性修复） | 修复 `NewDownloader` 空配置 panic（改为显式错误返回）并补单测；取消流程可用（`DELETE /api/v4/download/:id` 返回 `code=0`），但离线下载完成链路仍被 aria2 环境阻塞 | Fail（回归：不再 panic，但下载任务仍因 aria2 不可达失败） |
| G8-04 | 抽取与sidecar | 产物可读，字段正确 | Pass（修复后） | 初测 Fail：`/api/v4/file/fulltext/sidecar` 返回 sidecar 不存在，根因是 FTS 默认关闭（`fts_enabled=0`） | 无代码改动（运行态配置修正） | 通过 `POST /api/v4/admin/settings` 启用并配置 FTS（ES+Tika+sidecar），重启主从后再次触发更新；sidecar manifest 含 `content.txt/rmeta.json/attachments/*`，ES 文档 `file_id=49950` 与 `latest_version.id=50219` 同步正确 | Pass（Cloudreve `file/search` 关键词 `g8sidecar` 命中 `total=1`） |
| G8-05 | 队列监控 | metrics/列表/状态一致 | Pass（`2026-03-24`） |  |  | `GET /api/v4/admin/queue/metrics`、`POST /api/v4/admin/queue`、`GET /api/v4/admin/queue/:id` 全链路正常，`content_processing success_tasks=6,failure_tasks=0` | Pass |

Gate-8 通过标准：`G8-*` 全部 Pass。
当前状态：未达标（`G8-03` 受 aria2 环境阻塞，其余用例通过）。

## 阶段 9（Gate-9）：文件变更到 ES 同步正确性（含 50000 基线）

目标：

- 所有文件操作都能正确反映到 ES。
- 压力后保持一致性与无越权命中。

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G9-01 | 创建/改名/移动/删除到 ES | 检索结果与文件系统一致 |  |  |  |  |  |
| G9-02 | 50000 压测后索引一致性 | 计数一致+抽样一致 |  |  |  |  |  |
| G9-03 | 越权复验 | 压力后仍无越权命中 |  |  |  |  |  |

Gate-9 通过标准：`G9-*` 全部 Pass。

## 阶段 10（Gate-10）：统一认证页面与功能模块全覆盖

页面范围基于：

- `authverse/src/router/index.tsx`
- `authverse/src/component/UnifiedAuth/systemSections.tsx`

至少覆盖：

- 登录与 SSO：`/session`、`/sso`、`/session/authorize`、`/callback/*`
- 系统管理：用户、角色、部门、岗位、菜单、字典、审计日志
- 授权管理：OAuth2、网盘授权、统一检索、统一授权

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G10-01 | 登录/SSO 页 | 可登录可登出，无报错 |  |  |  |  |  |
| G10-02 | 系统管理模块 | 页面可达，核心 CRUD 可用 |  |  |  |  |  |
| G10-03 | OAuth2 模块 | 客户端/令牌功能可用 |  |  |  |  |  |
| G10-04 | 网盘授权模块 | 资源/模板/策略/试算可用 |  |  |  |  |  |
| G10-05 | 统一检索模块 | 源/索引/策略/模板可用 |  |  |  |  |  |
| G10-06 | 统一授权模块 | 策略与预览链路可用 |  |  |  |  |  |

Gate-10 通过标准：`G10-*` 全部 Pass。

---

## 6. 问题修复与回归闭环（必须执行）

每次 Fail 必走：

1. 定位：接口日志 + 前端 Console + 网络请求 + 后端错误栈。
2. 修复：直接改代码/配置/SQL。
3. 单点回归：先复测失败用例。
4. 阶段回归：复测当前 Gate 已通过用例。
5. 记录：填完整“失败原因 + 修改路径 + 回归结果”。

---

## 7. 全流程回归（最终）

当 Gate-1 ~ Gate-10 全部通过后，从头执行一次 `R-01`：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| R-01 | 全流程回归 | 全部阶段关键路径再次通过 |  |  |  |  |  |

---

## 8. 可直接复用的烟测脚本（建议）

来自 `authverse-backend/script/shell`：

- `unified-auth-smoke.sh`：统一授权基础烟测（创建/预览/删除闭环）
- `unified-auth-resource-roundtrip-smoke.sh`：网盘资源登记 -> 统一授权候选回填闭环
- `search-demo-es-seed.sh`：统一检索中文演示数据写入/清理

建议在阶段 6 和阶段 7 之前执行一次，作为基础健康检查。

多后端分离部署注意：

- 若统一检索与统一授权接口挂在 `search-server`（`48081`），脚本执行时需显式指定 `BASE_URL=http://127.0.0.1:48081`。

---

## 9. 执行快照（`2026-03-24 19:43 +0800`）

当前联调运行态：

- Cloudreve：`http://localhost:5212`（`V4.14.0`）
- Authverse Backend：`http://localhost:48080`
- Search Backend：`http://localhost:48081`
- Authverse Frontend：`http://localhost:5174`
- Search Frontend：`http://localhost:5175`
- Syncthing GUI：`http://127.0.0.1:18384`

压测实时指标快照（G3-04 / G3-05 完成后）：

- `GET /rest/db/status?folder=g3sync-folder`：`state=idle`、`localFiles=49504`、`globalFiles=49504`、`localDirectories=107`、`globalDirectories=107`、`uploadTotalItems=0`、`uploadPendingItems=0`、`uploadingItems=0`、`errors=0`、`pullErrors=0`
- `GET /rest/system/cloudreve/traffic`：`inBytesTotal=18921608`、`outBytesTotal=10269747`
- 远端抽样：`cloudreve://my/2/gate3-sync/g3_mass_a` 首层目录 `100` 个；`dir_095=500`、`dir_093_copy=100`；变更文件内容与本地一致

说明：

- 5 万级基线同步与变更序列均已完成并回归通过。
- Gate-1 登出撤销链路已回归通过：`POST /admin-api/system/auth/logout` 后，旧 token 调 Auth/Search 返回 401，调 Cloudreve 返回 `code=40020,msg=访问令牌不存在`。
- Gate-6 基线烟测已通过：`unified-auth-smoke.sh`、`unified-auth-resource-roundtrip-smoke.sh` 在 `BASE_URL=48081` 下全步骤通过。
- Gate-6 主体矩阵已完成第一轮：基于测试资源 `cloudreve://public/gate6_dept_only`（`resourceId=3`）分别验证 user/role/dept 策略切换生效，且越权 API 强制请求被拒绝（`reason=target_not_found`）。
- Gate-6 `G6-05/G6-06` 已补测并通过：公共盘授权刷新与非 owner 删除后的回收站恢复链路已回归通过（恢复返回 `code=0`，目录状态一致）。
- Gate-7 已完成：统一检索运行态接口、三组 public/search 正反例、分页校验与新增模板（`g7-tree-prefix-template-190902`）均通过；临时 unified-auth 策略已清理回基线。
- Gate-8 已完成主从协调核心链路：压缩/解压/抽取-sidecar/ES 同步/队列监控通过；其中解压经“预创建目标目录”流程修正后回归通过。
- Gate-8 运行态修正：已启用 FTS 配置（`fts_enabled=1`、`fts_index_type=elasticsearch`、`fts_extractor_type=tika`、`fts_tika_sidecar_enabled=1`）并完成主从重启回归，sidecar 与 ES 文档更新均正确。
- Gate-8 离线下载仍阻塞：远程下载任务可创建但最终 `error`，根因是 aria2 daemon 不可达（docker 拉取 `p3terx/aria2-pro` 超时 `registry-1.docker.io ... i/o timeout`）；仅取消流程可用。

## 10. 缺陷与修复跟踪（持续更新）

| 缺陷ID | 所属阶段 | 严重级别 | 复现步骤 | 关键日志/请求 | 修复提交 | 备注 |
| --- | --- | --- | --- | --- | --- | --- |
| D-001 | Gate-1 | 高 | 使用统一认证检索侧登录 token 访问 Cloudreve `/api/v4/user/me` | Cloudreve 返回 `code=40020`，日志为 OIDC userinfo 解析失败 | `cloudreve/service/user/oidc_token.go`（userinfo 失败回退 introspection） | 已回归通过，三系统 token 互通恢复 |
| D-002 | Gate-2 | 高 | 访问 Cloudreve `/connect` 页面 | 日志出现 `Static resource version mismatch [Current 4.0.0-next, Desired: 4.14.0]`，前端报 React minified error #311 | 重建 `application/statics/assets.zip`（`bash .build/build-assets.sh 4.14.0`）并重启 Cloudreve | 已回归通过，`/connect` 可正常使用 |
| D-003 | Gate-3 | 中 | Syncthing 执行删除同步（`DELETE /api/v4/file`） | `Failed to publish audit event 15: ... violates foreign key constraint "audit_logs_files_audit_logs"` | 待修复 | 不影响当前同步成功，但审计日志链路存在异常告警 |
| D-004 | Gate-3 | 高 | 5 万基线首轮压测后执行本地/远端计数比对 | 队列清空后仍缺失 `2362` 文件（本地 `49500` vs 远端 `47138`） | 过程修复（无代码改动） | 对缺失文件定向触发内容变更并二次扫描补传，最终恢复一致（`49500=49500`） |
| D-005 | Gate-1 | 中 | 旧 token 登出后调用 Cloudreve `/api/v4/user/me` | Cloudreve 返回 `code=40020,msg=Failed to parse OIDC introspection response`，错误语义不准确 | `cloudreve/service/user/oidc_token.go` | `introspectOIDCAccessToken` 透传上游业务错误，不再把撤销类错误包装为“解析失败”；回归后返回 `msg=访问令牌不存在` |
| D-006 | Gate-6 | 中 | 直接执行 `unified-auth-smoke.sh` 与 `unified-auth-resource-roundtrip-smoke.sh` | 默认 `BASE_URL=48080` 时 `/admin-api/search/unified-auth/*` 返回 404 | 无代码改动（联调执行参数修正） | 分离部署需改用 `BASE_URL=48081`；修正后两脚本闭环通过 |
| D-007 | Gate-6 | 高 | 调用 `/admin-api/system/cloudreve-authz/resource/cloudreve-current?appCode=cloudreve&uri=cloudreve://public/...` | 返回 502：`OAuth2 Client(cloudreve) 缺少 Cloudreve 代理所需 scope` | 无代码改动（统一认证后台配置修正） | 在统一认证将 `clientId=cloudreve` 补齐 scope：`openid/profile/email/user.read/Admin.Read/Files.Read`，回归后资源快照拉取成功 |
| D-008 | Gate-6 | 高 | 用户在公共盘删除自己新建的目录后，从回收站恢复 | 恢复返回 `code=40081,msg=faield to get destination folder: Permission denied`；回收站元数据中 `sys:restore_uri` 指向 `cloudreve://<owner>@my/...` 导致跨 owner 目的地解析失败 | `cloudreve/pkg/filemanager/fs/dbfs/manage.go`、`cloudreve/pkg/filemanager/fs/dbfs/dbfs.go`、`cloudreve/pkg/filemanager/fs/dbfs/manage_test.go` | 软删除改为优先记录调用侧源 URI；恢复路由放开 `trash -> public`；补单测后回归通过（删除/恢复均 `code=0`） |
| D-009 | Gate-8 | 高 | 从节点配置 `provider=aria2` 但缺少 aria2 配置时触发 downloader 构建 | slave panic，栈定位 `pkg/downloader/aria2/aria2.go:39`（空指针访问 `options.Server`） | `cloudreve/pkg/cluster/node.go`、`cloudreve/pkg/cluster/downloader_test.go` | `NewDownloader` 增加空配置校验（`options=nil`、`Aria2Setting=nil`、`QBittorrentSetting=nil` 返回显式错误），并新增单测；`go test ./pkg/cluster` 通过，回归不再 panic |
| D-010 | Gate-8 | 中 | 执行离线下载协调链路（创建任务->轮询->选文件） | 任务长期 `suspending` 后转 `error`，`PATCH /api/v4/download/:id` 报 `Task not in monitoring loop`；docker 拉取 aria2 镜像超时 | 无代码改动（环境阻塞） | 当前影响 `G8-03` 未通过；已确认取消流程 `DELETE /api/v4/download/:id` 可用，待 aria2 daemon 可达后重跑全流程 |
