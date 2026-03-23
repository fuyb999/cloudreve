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
| G1-01 | OIDC prepare 与跳转 | 跳转 URL 正确，进入 `/sso` |  |  |  |  |  |
| G1-02 | OIDC exchange | 回调换票成功，进入 `/home` |  |  |  |  |  |
| G1-03 | 三系统 token 互通 | 三个代表接口都 200 |  |  |  |  |  |
| G1-04 | 登出与撤销 | 旧 token 失效，回到登录态 |  |  |  |  |  |

Gate-1 通过标准：`G1-*` 全部 Pass。

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
| G2-01 | 授权页拉起 | `session/authorize` 正常 |  |  |  |  |  |
| G2-02 | 回调换票 | Syncthing 状态 authorized |  |  |  |  |  |
| G2-03 | 设备出现与心跳 | `/devices/syncthing` 在线刷新 |  |  |  |  |  |

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
| G3-01 | 新建同步 | 双端出现且属性一致 |  |  |  |  |  |
| G3-02 | 移动/复制/重命名同步 | 路径与文件内容一致 |  |  |  |  |  |
| G3-03 | 删除同步 | 远端删除成功（同步链路硬删） |  |  |  |  |  |
| G3-04 | 50000 基线同步 | 计数一致、无持续错误 |  |  |  |  |  |
| G3-05 | 50000 变更序列 | 变更后一致性通过 |  |  |  |  |  |

Gate-3 通过标准：`G3-*` 全部 Pass。

## 阶段 4（Gate-4）：Syncthing 设备注册/解绑/配置恢复/冲突

重点切入点：

- `cloudreve/service/setting/syncthing.go`
- `cloudreve/inventory/syncthing_device.go`
- 错误语义：`ErrSyncthingDeviceIPConflict`、`ErrSyncthingDeviceNotRegistered`

用例表：

| 用例ID | 测试项 | 预期结果 | 实际结果（Pass/Fail） | 不通过原因 | 修改路径 | 修改说明 | 回归结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G4-01 | 注册与心跳 | online/last_seen 刷新 |  |  |  |  |  |
| G4-02 | 非永久解绑 | 设备变 unbound，可恢复 |  |  |  |  |  |
| G4-03 | 配置恢复 | 同 IP 新设备拿到 restore_config |  |  |  |  |  |
| G4-04 | 永久删除 | 删除后无法恢复配置 |  |  |  |  |  |
| G4-05 | 同 IP 冲突 | 返回冲突错误，禁止注册 |  |  |  |  |  |

Gate-4 通过标准：`G4-*` 全部 Pass。

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
| G5-01 | 新建/上传 | 创建成功并可见 |  |  |  |  |  |
| G5-02 | 重命名/移动/复制 | 文件树与元数据一致 |  |  |  |  |  |
| G5-03 | 删除/恢复 | 回收站语义正确 |  |  |  |  |  |
| G5-04 | 分享/直链 | 权限与有效期正确 |  |  |  |  |  |
| G5-05 | 压缩/解压/版本 | 工作流正确，结果可用 |  |  |  |  |  |

Gate-5 通过标准：`G5-*` 全部 Pass。

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
| G6-01 | 用户主体策略 | 可见性与动作符合模板 |  |  |  |  |  |
| G6-02 | 角色主体策略 | 可见性与动作符合模板 |  |  |  |  |  |
| G6-03 | 部门层级策略 | 上下级关系判定正确 |  |  |  |  |  |
| G6-04 | API 越权防护 | 强行请求被拒绝 |  |  |  |  |  |
| G6-05 | 公共盘新建后权限刷新 | 动作按钮即时正确 |  |  |  |  |  |
| G6-06 | 公共盘删除与回收站 | 可见性与恢复语义正确 |  |  |  |  |  |

Gate-6 通过标准：`G6-*` 全部 Pass，且无 API 越权成功。

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
| G7-01 | 来源与概览接口 | `/sources` `/overview` `/dashboard` 正常 |  |  |  |  |  |
| G7-02 | 运行态查询 | `/query` 命中与分页正确 |  |  |  |  |  |
| G7-03 | 越权负例1 | public可见+search deny 不命中 |  |  |  |  |  |
| G7-04 | 越权负例2 | public不可见+search allow 不命中 |  |  |  |  |  |
| G7-05 | 正例 | public可见+search allow 命中 |  |  |  |  |  |
| G7-06 | 新增策略模板 | 模板可套用并生效 |  |  |  |  |  |

Gate-7 通过标准：`G7-*` 全部 Pass。

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
| G8-01 | 压缩协调 | 主从分发与产物正确 |  |  |  |  |  |
| G8-02 | 解压协调 | 主从分发与目录正确 |  |  |  |  |  |
| G8-03 | 离线下载协调 | 创建/取消/选文件正常 |  |  |  |  |  |
| G8-04 | 抽取与sidecar | 产物可读，字段正确 |  |  |  |  |  |
| G8-05 | 队列监控 | metrics/列表/状态一致 |  |  |  |  |  |

Gate-8 通过标准：`G8-*` 全部 Pass。

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
