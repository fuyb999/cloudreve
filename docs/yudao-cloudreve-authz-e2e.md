# Yudao Cloudreve 授权中心联调说明

本文档用于说明 `Yudao` 中 `Cloudreve 授权中心` 当前版本的落地方式、前端点击路径、数据库初始化要求，以及 2026 年 3 月 17 日这次真实联调的结果。

## 1. 适用范围

当前文档对应的是这一版能力：

- `Yudao` 后台提供 `Cloudreve 授权中心`
- 支持公共资源建模
- 支持策略模板库
- 支持规则模板参数可视化配置器
- 支持模板分页、详情、新增、修改、删除
- 支持资源绑定策略
- 支持资源归档 / 启用 / 删除
- 支持策略归档 / 启用 / 删除
- 支持本地动作试算

前端页面入口对应：

- 菜单组件：`system/cloudreve/authz/index`
- Vue 组件名：`SystemCloudreveAuthz`

## 2. 启动前检查

联调前至少确认下面 4 项：

1. PostgreSQL 已执行授权中心升级脚本。
2. PostgreSQL 已执行授权中心菜单脚本。
3. Yudao 后端使用的是最新打包后的 `yudao-server.jar`。
4. Yudao 前端使用的是当前最新 `yudao-ui-admin-vue3` 代码。

## 3. PostgreSQL 初始化

### 3.1 必执行脚本

先执行表结构脚本：

- `/home/develop/IdeaProjects/yudao/ruoyi-vue-pro-master-jdk17/ruoyi-vue-pro-master-jdk17/sql/postgresql/cloudreve-authz-upgrade.sql`

再执行菜单脚本：

- `/home/develop/IdeaProjects/yudao/ruoyi-vue-pro-master-jdk17/ruoyi-vue-pro-master-jdk17/sql/postgresql/cloudreve-authz-menu.sql`

如果只执行了菜单脚本，没有执行升级脚本，会出现下面这个典型问题：

```text
ERROR: relation "system_cloudreve_authz_policy_template" does not exist
```

这意味着模板表没有创建，模板分页、模板创建、模板目录加载都会失败。

### 3.2 本机数据库连接

当前本地联调使用的是：

```text
host: localhost
port: 5432
database: ruoyi-vue-pro
username: cloudreve
password: cloudreve
```

### 3.3 如果机器上没有 `psql`

本机这次联调时没有 `psql` 命令，最终是通过 `jshell + PostgreSQL JDBC` 执行 SQL。

可直接使用：

```bash
export JAVA_HOME=/data/container-apps/jdk-17.0.10
export PATH=$JAVA_HOME/bin:$PATH

jshell --class-path /home/develop/.m2/repository/org/postgresql/postgresql/42.7.8/postgresql-42.7.8.jar <<'EOF'
import java.sql.*;
import java.nio.file.*;
String sql = Files.readString(Path.of("/home/develop/IdeaProjects/yudao/ruoyi-vue-pro-master-jdk17/ruoyi-vue-pro-master-jdk17/sql/postgresql/cloudreve-authz-upgrade.sql"));
try (Connection conn = DriverManager.getConnection("jdbc:postgresql://localhost:5432/ruoyi-vue-pro", "cloudreve", "cloudreve");
     Statement stmt = conn.createStatement()) {
    stmt.execute(sql);
    System.out.println("cloudreve-authz-upgrade.sql executed");
}
EOF
```

菜单脚本也可以用同样方式执行。

## 4. 启动方式

### 4.1 Yudao 后端

先重新打包：

```bash
export JAVA_HOME=/data/container-apps/jdk-17.0.10
export PATH=$JAVA_HOME/bin:$PATH

cd /home/develop/IdeaProjects/yudao/ruoyi-vue-pro-master-jdk17/ruoyi-vue-pro-master-jdk17
/data/container-apps/apache-maven-3.9.6/bin/mvn -pl yudao-server -am -DskipTests package
```

再启动：

```bash
java -jar /home/develop/IdeaProjects/yudao/ruoyi-vue-pro-master-jdk17/ruoyi-vue-pro-master-jdk17/yudao-server/target/yudao-server.jar --spring.profiles.active=local
```

本地联调端口：

```text
http://127.0.0.1:48080
```

### 4.2 Yudao 前端

前端开发服务本地端口：

```text
http://127.0.0.1:5173
```

如果未启动，可在下面目录启动：

```bash
cd /home/develop/IdeaProjects/yudao/yudao-ui-admin-vue3
npm run dev
```

## 5. 前端点击路径

登录 `Yudao` 管理后台后，进入：

```text
系统管理 -> Cloudreve 授权中心
```

如果菜单没有出现，优先检查：

1. 是否执行了 `cloudreve-authz-menu.sql`
2. 当前账号是否分配了以下权限：

```text
system:cloudreve-authz:resource:query
system:cloudreve-authz:resource:update
system:cloudreve-authz:policy:query
system:cloudreve-authz:policy:update
```

## 6. 联调步骤

### 6.1 模板管理

进入授权中心后，先点击顶部按钮：

```text
模板管理
```

验证点：

1. 能打开模板管理弹窗。
2. 列表里能看到 6 个系统内置模板。
3. 能新增自定义模板。
4. 能编辑自定义模板。
5. 能删除自定义模板。
6. 模板参数区域已升级成“规则模板参数可视化配置器”，可以通过参数卡片 + 右侧属性面板维护参数。
7. 编辑或删除后，关闭弹窗再打开策略抽屉，模板目录应是最新数据。

这一版系统内置模板包括：

- `public-readonly`
- `public-owner-maintain`
- `public-admin-owner-maintain`
- `dept-hierarchy-readonly`
- `dept-hierarchy-role-maintain`
- `role-workspace`

### 6.2 新增资源

回到授权中心主列表，点击：

```text
新增资源
```

推荐操作：

1. 先选择 `Cloudreve` 应用编码，例如 `cloudreve-main`
2. 如果已打通远程浏览，可用“浏览公共资源”或“读取当前 URI”回填
3. 确认 `fileId / ownerId / treePath / cloudreveUri` 与实际资源一致
4. 保存后点击“保存并配置策略”

### 6.3 配置策略

进入策略抽屉后：

1. 先确认资源信息是否正确
2. 选择一个策略模板
3. 如模板带参数，先填写参数
4. 点击“套用模板”
5. 检查可见性规则和动作规则是否符合预期
6. 点击“保存”

### 6.4 正式清理流程

联调结束后，不再需要手工执行 SQL 清理数据，可直接在资源列表操作列完成：

1. 对资源执行“归档资源”，验证归档后资源立即退出授权计算。
2. 对资源执行“启用资源”，验证恢复后继续沿用原策略。
3. 对策略执行“归档策略”或“启用策略”，验证策略状态切换能立即影响判定。
4. 对策略执行“删除策略”，验证资源仍保留但授权规则已清空。
5. 对资源执行“删除资源”，验证资源与其当前绑定策略一起被逻辑删除。

### 6.5 本地试算

策略抽屉下方有“本地即时试算”和“策略模拟”两块：

1. 本地即时试算：不保存到后端，直接按当前编辑对象做前端推演
2. 策略模拟：调用后端 `/policy/simulate`，使用当前登录管理员身份做真实判定

联调时建议至少验证：

- `download`
- `upload`
- `create`
- `rename`
- `delete`

## 7. 成功判定

满足下面条件，说明这一版前后端主链路是通的：

1. 模板分页正常返回。
2. 模板新增、修改、删除都成功，且参数配置器可正常保存参数定义。
3. 新增资源成功。
4. 资源策略保存成功。
5. `/policy/get-by-resource` 能读回刚保存的规则。
6. `/policy/simulate` 返回 `allowed` 和动作矩阵，且结果符合预期。
7. 资源 / 策略归档、启用、删除都能从前端直接完成。

## 8. 2026-03-17 真实联调结果

本次已完成真实接口联调，结论如下：

### 8.1 已验证成功

- 模板创建成功
- 模板分页成功
- 模板详情成功
- 模板修改成功
- 模板删除成功
- 资源创建成功
- 策略保存成功
- 后端动作试算成功

### 8.2 真实命中结果

联调时曾创建一组临时测试数据，并验证：

- `upload` 返回 `allowed = true`
- `download` 返回 `allowed = true`
- `nearest_policy_allowed` 命中最近策略

### 8.3 清理结果

联调完成后，清理流程已经具备正式功能入口：

1. 自定义模板通过模板管理页直接删除。
2. 临时策略通过资源列表“删除策略”直接删除。
3. 临时资源通过资源列表“删除资源”直接逻辑删除，并联动删除策略。

清理后再次校验结果：

- 模板分页按测试模板编码查询：`total = 0`
- 资源分页按测试 `fileId` 查询：`total = 0`

## 9. 当前已知限制

### 9.1 模板表是新增表

如果从旧环境升级，一定要补执行 PG 升级脚本，否则前端“模板管理”和策略抽屉里的模板目录都会失败。

### 9.2 当前清理入口位于资源主列表

当前前端已经具备正式清理能力，但入口集中在资源主列表“更多”菜单，而不是单独的清理页面：

- 模板新增/编辑/删除
- 资源新增/编辑/归档/启用/删除
- 策略保存/模拟/归档/启用/删除

如果后续要做更强的运维能力，建议再补：

1. 独立的“归档资源视图”
2. 按应用编码批量清理的筛选器
3. 资源 / 策略操作日志

### 9.3 后端要用最新打包产物

这次联调过程中确认过一个问题：

- 如果直接运行旧的 `yudao-server.jar`
- 即使源码已经有 `policy/template/page` 等接口
- 实际运行进程里仍可能没有这些新接口

所以变更后要先重新 `package`，再启动最新 jar。

## 10. 建议的下一步

当前代码和接口已经具备继续推进下面两项的条件：

1. 把模板参数设计器继续升级成“规则模板参数 + AST 片段联动构建器”
2. 增加资源 / 策略操作审计，补齐“谁归档、谁删除、何时恢复”的追踪链路
