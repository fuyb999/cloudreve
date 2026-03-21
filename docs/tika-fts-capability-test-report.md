# Tika 全文抽取能力测试报告

本文档记录 2026 年 3 月 21 日基于本地 `Cloudreve + PostgreSQL + Elasticsearch + Apache Tika` 真实环境完成的一轮 Tika 抽取能力测试，重点覆盖：

- Tika 原生 API 能力
- Cloudreve 接入 Tika 后的全文抽取、PG sidecar、ES 同步链路
- 文档、邮件、归档、多层压缩包、中文文件名、不同编码文本的边界行为
- 已发现限制及建议解决方案

## 1. 本轮结论

结论先行：

- `DOCX/PDF/EML/MBOX/WINMAIL.DAT` 已在真实链路验证通过，文本、图片/附件、层级关系、PG sidecar、ES 同步都能跑通。
- 单文件文本在 `UTF-8/GBK/GB2312` 三种编码下均已验证可正确提取内容。
- 递归压缩包中，`UTF-8 ZIP` 与 `UTF-8 7z` 的中文目录名、中文文件名、正文内容都可正确保留。
- `GBK/GB2312 ZIP` 的正文内容可提取，但内层中文目录名、中文文件名会乱码。
- `UTF-8 RAR` 中文递归场景在当前 Tika 容器下会直接返回 `422`，Cloudreve 侧不会生成 sidecar 附件树。
- 本轮已确认问题不在 ES/PG 落盘逻辑，而在 Tika 对特定编码压缩包或 RAR Unicode 场景的原生解析能力。

## 2. 测试环境

本地测试环境：

- `Cloudreve` 当前工作区代码
- `PostgreSQL`: `127.0.0.1:5432`
- `Elasticsearch`: `http://127.0.0.1:9200`
- `Tika`: `http://localhost:9998`

Tika 实际版本：

```text
Apache Tika 3.2.3
```

当前自定义 Tika 容器基于：

- `docker/tika-unrar/Dockerfile`
- 基础镜像：`apache/tika:3.2.3.0-full`
- 额外安装：`unrar-free`
- 自定义配置：`docker/tika-unrar/tika-config.xml`

当前容器做了 `RarParser -> UnrarParser` 切换：

```xml
<parser class="org.apache.tika.parser.DefaultParser">
  <parser-exclude class="org.apache.tika.parser.pkg.RarParser"/>
</parser>
<parser class="org.apache.tika.parser.pkg.UnrarParser">
  <mime>application/x-rar-compressed</mime>
  <mime>application/vnd.rar</mime>
</parser>
```

## 3. 官方能力与本地实际能力

官方文档可参考：

- Tika 3.2.3 支持格式页：<https://tika.apache.org/3.2.3/formats.html>
- Tika Server JAX-RS 资源文档：
  - `/tika` 对应 `TikaResource`
  - `/rmeta` 对应 `RecursiveMetadataResource`
  - `/unpack`、`/unpack/all` 对应 `UnpackerResource`
  - `/parsers/details` 对应 `TikaParsers`

本地真实接口 `GET /parsers/details` 返回结果中，已确认存在以下关键解析器：

- `org.apache.tika.parser.pdf.PDFParser`
- `org.apache.tika.parser.microsoft.ooxml.OOXMLParser`
- `org.apache.tika.parser.microsoft.OfficeParser`
- `org.apache.tika.parser.microsoft.TNEFParser`
- `org.apache.tika.parser.mail.RFC822Parser`
- `org.apache.tika.parser.mbox.MboxParser`
- `org.apache.tika.parser.pkg.PackageParser`
- `org.apache.tika.parser.pkg.CompressorParser`
- `org.apache.tika.parser.pkg.UnrarParser`

本地 `parsers/details` 的关键 MIME 支持摘要：

| 解析器 | 本地已确认支持的代表 MIME |
| --- | --- |
| `PDFParser` | `application/pdf` |
| `OOXMLParser` | `docx/xlsx/pptx/visio` 等 OOXML |
| `OfficeParser` | `application/msword`、`application/vnd.ms-powerpoint`、`application/vnd.ms-project`、`application/x-mspublisher` 等 |
| `TNEFParser` | `application/vnd.ms-tnef`、`application/x-tnef`、`application/ms-tnef` |
| `RFC822Parser` | `message/rfc822` |
| `MboxParser` | `application/mbox` |
| `PackageParser` | `application/zip`、`application/x-tar`、`application/x-7z-compressed`、`application/java-archive`、`application/x-cpio`、`application/x-arj`、`application/x-archive`、`application/x-tika-unix-dump` |
| `CompressorParser` | `gzip/bzip2/compress/lzma/lz4/snappy/brotli/pack200` 等 |
| `UnrarParser` | `application/x-rar-compressed` |

## 4. Cloudreve 当前配置入口

本次能力在 Cloudreve 中对应的参数入口：

- `fts_tika_document_enabled`
- `fts_tika_document_exts`
- `fts_tika_archive_enabled`
- `fts_tika_archive_exts`
- `fts_tika_sidecar_enabled`
- `fts_tika_sidecar_text_enabled`
- `fts_tika_sidecar_assets_enabled`
- `fts_tika_extract_inline_images`

当前默认扩展名配置：

### 4.1 文档类

字段：`fts_tika_document_exts`

```text
pdf,txt,text,md,markdown,csv,tsv,html,htm,xhtml,xml,rtf,epub,fb2,chm,mif,doc,dot,docx,docm,dotx,dotm,wps,wks,wri,hwp,one,wpd,xls,xlt,xla,xlc,xlm,xlw,xlsx,xlsm,xltx,xltm,xlsb,xlam,qpw,ppt,pps,pot,pptx,pptm,ppsx,ppsm,potx,potm,sldx,sldm,ppam,vsd,vst,vss,vsdx,vstx,vssx,vsdm,vstm,vssm,pub,mpp,xps,dwfx,odt,fodt,ott,odm,oth,ods,fods,ots,odp,fodp,otp,odg,fodg,otg,odc,odf,odb,odi,sxw,stw,sxg,sxc,stc,sxi,sti,sxd,std,sxm,pages,numbers,key,eml,mht,mhtml,nws,msg,pst,mbox,tnef
```

### 4.2 压缩包类

字段：`fts_tika_archive_exts`

```text
zip,tar,tgz,tbz,tbz2,txz,tlz,7z,rar,ar,gz,z,bz,bz2,xz,lzma,lz4,br,snappy,sz,pack200,cpio,arj,dump,jar,war,ear
```

说明：

- 使用时应根据 `document/archive` 两个开关分别决定是否启用。
- 当前实现已经把文档能力和压缩包能力拆成两个配置项，避免全部能力绑在一个大列表上。

## 5. 本轮调用的 Tika API

### 5.1 `PUT /tika`

用途：

- 提取纯文本内容

关键请求头：

- `Accept: text/plain`
- `Content-Disposition: attachment; filename="xxx.ext"`
- `Content-Type: xxx/yyy`

典型调用：

```bash
curl -X PUT 'http://localhost:9998/tika' \
  -H 'Accept: text/plain' \
  -H 'Content-Disposition: attachment; filename="testWINMAIL.dat"' \
  -H 'Content-Type: application/vnd.ms-tnef' \
  --data-binary @testWINMAIL.dat
```

本轮实际观察：

- `gbk-中文.txt` 在 `Content-Type: text/plain` 下可直接返回正确中文正文
- `testWINMAIL.dat` 可返回正文和嵌入附件文本
- `utf8-中文.rar` 返回 `422`

### 5.2 `PUT /rmeta`

用途：

- 递归提取元数据与嵌入对象清单

关键请求头：

- `Accept: application/json`
- `Content-Disposition`
- `Content-Type`

本轮对 `testWINMAIL.dat` 调用后，返回 7 个对象：

- 根对象：`application/vnd.ms-tnef`
- 嵌入对象：`message.rtf`
- 嵌入对象：`quick.doc`
- 嵌入对象：`quick.html`
- 嵌入对象：`quick.pdf`
- 嵌入对象：`quick.txt`
- 嵌入对象：`quick.xml`

### 5.3 `PUT /unpack`

用途：

- 导出顶层 unpack 结果，返回 zip

### 5.4 `PUT /unpack/all`

用途：

- 导出递归 unpack 结果，返回 zip

关键请求头：

- `Accept: application/zip`
- `Content-Disposition`
- `Content-Type`

本轮对 `utf8-中文.zip` 调用 `/unpack/all` 后，zip 内部对象名为：

```text
__TEXT__
中文目录/二级目录/中文内容.txt
中文目录/外层说明.txt
__METADATA__
```

说明：

- UTF-8 ZIP 在 `unpack/all` 里可保留正确中文路径。

### 5.5 `GET /parsers/details`

用途：

- 查看当前服务实际加载的解析器与 MIME 支持集

关键请求头：

- `Accept: application/json`

该接口非常适合用于：

- 部署验收
- 比对 `full` 镜像和自定义镜像是否真的加载了指定 parser
- 判断当前容器是否真的启用了 `UnrarParser`

## 6. 测试数据

### 6.1 固定样本

- `.tmp/testdata/testWINMAIL.dat`
- `.tmp/testdata/smoke.eml`
- `.tmp/testdata/smoke.mbox`
- `.tmp/testdata/utf8-中文.txt`
- `.tmp/testdata/gbk-中文.txt`
- `.tmp/testdata/gb2312-中文.txt`

### 6.2 运行时构造样本

由 `.tmp/fts_real_smoke.go` 动态生成：

- 最小 `DOCX`，包含正文 + `word/media/image1.png`
- 最小 `PDF`，包含正文 + inline image + embedded file `attached.txt`
- 多层归档：
  - `utf8-中文.zip`
  - `gbk-中文.zip`
  - `gb2312-中文.zip`
  - `utf8-中文.7z`
  - `utf8-中文.rar`

边界正文统一使用：

```text
中文边界测试 你好，世界
第二行：压缩包中文名 <suffix>
```

## 7. Cloudreve 全链路真实测试结果

本节结果来自：

- `go run .tmp/fts_real_smoke.go`
- 输出日志：`/tmp/fts_real_smoke_latest.log`

### 7.1 文档、邮件、容器类文件

| 文件类型 | 结果 | 说明 |
| --- | --- | --- |
| `DOCX` | 通过 | 正文可提取，`image1.png` 可落 sidecar 和 ES 附件 |
| `PDF` | 通过 | 正文可提取，inline image 可提取，embedded `attached.txt` 可提取 |
| `WINMAIL.DAT` | 通过 | `TNEFParser` 生效，正文和 6 个典型附件均可提取 |
| `EML` | 通过 | 正文与 `eml-attachment.txt` 可提取 |
| `MBOX` | 通过 | 可递归出 `0.eml`，正文与附件文本可提取 |

### 7.2 单文件文本编码边界

| 文件 | 编码 | 正文提取 | 文件名 | 结论 |
| --- | --- | --- | --- | --- |
| `utf8-中文.txt` | UTF-8 | 正确 | 正确 | 通过 |
| `gbk-中文.txt` | GBK | 正确 | 正确 | 通过 |
| `gb2312-中文.txt` | GB2312 | 正确 | 正确 | 通过 |

说明：

- 这一项依赖 Cloudreve 侧的一个修复：不要把 `mime.TypeByExtension()` 自动带出的 `charset=utf-8` 强塞给所有文本文件。
- 修复后，Tika 能自行探测 `GBK/GB2312` 的正文编码。

### 7.3 多层压缩包中文边界

| 文件 | 中文正文 | 外层中文名 | 内层中文名 | 递归层级 | PG sidecar | ES attachments | 结论 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `utf8-中文.zip` | 正确 | 正确 | 正确 | 正确 | 有 | 有 | 通过 |
| `gbk-中文.zip` | 正确 | 正确 | 乱码 | 正确 | 有 | 有 | 部分通过 |
| `gb2312-中文.zip` | 正确 | 正确 | 乱码 | 正确 | 有 | 有 | 部分通过 |
| `utf8-中文.7z` | 正确 | 正确 | 正确 | 正确 | 有 | 有 | 通过 |
| `utf8-中文.rar` | 失败 | 失败 | 失败 | 失败 | 无有效 sidecar | 无附件树 | 不通过 |

实际观察摘要：

- `utf8-中文.zip`
  - `外层说明.txt`
  - `内层.zip`
  - `中文内容.txt`
  - `parent_attachment_id="attachments/中文目录/内层.zip"`
  - 层级完全正确
- `gbk-中文.zip` / `gb2312-中文.zip`
  - 正文内容仍然能进入 `content`
  - 外层 `外层说明.txt` 与 `内层.zip` 名字仍正常
  - 内层 `二级目录/中文内容.txt` 会变成 `����Ŀ¼/��������.txt`
- `utf8-中文.7z`
  - 顶层和内层 ZIP 中的中文路径都保留正确
  - ES 附件树里能看到两个 `中文内容.txt`，一个是 7z 顶层文本，一个是内层 ZIP 里的递归文本
- `utf8-中文.rar`
  - Tika 原生 `/tika` 直接返回 `422`
  - Cloudreve 日志也表现为 `tika returned status 422`
  - 只保留了空的主文档，没有成功产出附件树

### 7.4 ASCII 递归压缩包回归结果

同一轮真实 smoke 中，以下递归归档场景继续通过：

- `zip`
- `tar`
- `jar`
- `tgz`
- `tbz2`
- `txz`
- `lzma`
- `ar`
- `cpio`
- `7z`
- `rar`

说明：

- `rar` 的 ASCII 递归场景可通过，不代表 Unicode/中文路径的 RAR 递归也可通过。
- 当前 RAR 问题是“中文/Unicode 边界失败”，不是“RAR 全部不可用”。

## 8. PG sidecar 与 ES 同步结果

### 8.1 PG sidecar

本轮已验证：

- sidecar 清单文件会落到存储策略路径
- 文本与附件对象不会直接以二进制塞入数据库
- PG 中保留的是 manifest 路径、entity 关联和索引关联信息
- sidecar 中对象可保持递归层级

例如 `utf8-中文.zip` 的附件层级：

- `attachments/中文目录/外层说明.txt`
- `attachments/中文目录/内层.zip`
- `attachments/中文目录/内层.zip/二级目录/中文内容.txt`

当前 ES 附件字段与 PG sidecar 的层级能保持一致，依赖字段包括：

- `attachments[].id`
- `attachments[].parent_attachment_id`
- `attachments[].depth`
- `attachments[].path`

### 8.2 ES 同步与文件操作

本轮真实 smoke 已覆盖并通过：

- 上传
- 文件重命名
- 文件移动
- 文件复制
- 文件夹移动
- 文件夹重命名
- 文件夹复制
- 软删除
- 从回收站恢复
- 硬删除
- 批量移动 16 个文件
- 批量文件夹重命名
- 批量软删除
- 批量恢复

结论：

- 只要 Tika 本身能成功抽取，PG sidecar 与 ES 同步链路是稳定的。
- 当前边界问题主要集中在 Tika 对特殊编码归档的解析，不在 ES/PG 同步本身。

## 9. 已发现问题与解决方案

### 9.1 问题一：`GBK/GB2312 TXT` 曾被错误按 UTF-8 发送

现象：

- 若按扩展名得到 `text/plain; charset=utf-8` 后强制设置给所有文本文件，Tika 会被误导，导致 `GBK/GB2312` 文本提取失败。

已完成修复：

- `pkg/searcher/extractor/tika.go`
- 新增 `normalizeTextLikeContentType`
- 对 `mime.TypeByExtension()` 返回值去掉强制 `charset=utf-8`

修复后结果：

- `UTF-8/GBK/GB2312` 单文件文本全部提取正确

### 9.2 问题二：`GBK/GB2312 ZIP` 内层中文名乱码

现象：

- 正文仍可被提取
- 外层名字在部分情况下仍正常
- 内层中文目录名、文件名会乱码

原因判断：

- 这是 Tika 递归处理 legacy ZIP 文件名编码时的限制。
- 当前更像是“ZIP entry name 的编码恢复问题”，不是正文解码问题。

建议解决方案：

1. 不把该问题继续硬压在 Tika 上处理。
2. Cloudreve 侧对 ZIP 增加“编码感知预解包”兜底。
3. 复用已有 ZIP 编码能力：
   - `pkg/filemanager/manager/archive.go`
   - `ZipEncodings`
   - `ListArchiveFiles(ctx, uri, entity, zipEncoding string)`
4. 在用户或存储策略明确指定 `gbk/gb18030/...` 时：
   - 由 Cloudreve 先按指定编码读取 ZIP entry name
   - 再自行递归展开 sidecar/attachments 树
   - 最后把文本写入 ES `content`，附件写入 ES `attachments`
5. Tika 在这里更适合负责“文件正文抽取”，不适合负责“legacy ZIP 文件名纠错”。

### 9.3 问题三：`UTF-8 RAR` 中文递归返回 `422`

现象：

- Tika 原生 `PUT /tika` 对 `utf8-中文.rar` 返回 `422`
- Cloudreve 真实链路中也同步失败
- 当前自定义容器已启用 `UnrarParser`，但仍失败

原因判断：

- 这是当前 `Tika + UnrarParser + unrar-free` 组合在 Unicode/中文 RAR 边界上的兼容性问题。
- 这不是 ES/PG 同步问题，也不是 Cloudreve sidecar 设计问题。

建议解决方案：

1. 短期：
   - 将 `rar` 保持为可配置项，不要把它视为“强保证能力”。
   - 生产上若追求稳定，可默认关闭 `archive` 中的 `rar`，或仅作为 best-effort。
2. 中期：
   - 把容器中的 `unrar-free` 替换为能力更完整的 `unrar`，然后重新做一轮 Unicode RAR 验证。
   - 这一点需要注意镜像许可和发版策略。
3. 长期：
   - Cloudreve 侧为 `RAR` 做服务端预解包兜底。
   - 成功解包后按与 ZIP 相同的 sidecar 递归树写入 PG/ES。
   - 若解包失败，则只保留主文档记录，不写错误附件树。

## 10. 对网盘抽取能力的建议描述

建议在产品或运维文档中这样描述：

### 10.1 文档能力

- 支持常见 Office、OpenDocument、PDF、文本、邮件、TNEF、Apple iWork 等文档类文件的正文抽取。
- 对 `DOCX/PDF/EML/MBOX/WINMAIL.DAT` 已完成真实环境验证。
- `PDF` 支持抽取正文、嵌入附件、内联图片。
- `WINMAIL.DAT` 支持递归抽取正文和附件。

### 10.2 压缩包能力

- 支持 `zip/tar/tgz/tbz2/txz/lzma/ar/cpio/7z/jar/war/ear` 等归档或压缩容器的递归抽取。
- 对 UTF-8 编码的 ZIP/7z 中文递归能力已完成真实验证。
- 对 legacy ZIP 文件名编码场景，正文可抽取，但文件名可能乱码。
- 对 RAR：
  - ASCII 递归场景已验证通过
  - UTF-8 中文 RAR 递归当前不稳定，不应宣称为强保证能力

### 10.3 数据落盘方式

- 文本与附件二进制不直接入数据库。
- 二进制内容按用户存储策略写入 sidecar 路径。
- PG 保存 manifest 与实体关联。
- ES 主文档写入 `content`，附件写入 `attachments`，并通过 `parent_attachment_id` / `depth` 保留层级关系。

## 11. 复现命令

### 11.1 全链路真实 smoke

```bash
GOCACHE=/tmp/go-build GOTMPDIR=/tmp go run .tmp/fts_real_smoke.go
```

### 11.2 提取边界结果

```bash
rg -n "boundary text|boundary archive|utf8 chinese rar|utf8 chinese 7z|gbk chinese zip|gb2312 chinese zip" /tmp/fts_real_smoke_latest.log
```

### 11.3 直接探测 Tika 版本

```bash
curl http://localhost:9998/version
```

### 11.4 直接探测 parser 装载情况

```bash
curl http://localhost:9998/parsers/details -H 'Accept: application/json'
```

## 12. 最终结论

当前这套方案已经可以在网盘场景中稳定支撑：

- 文档正文抽取
- 文档内图片/附件抽取
- 邮件与 TNEF 附件抽取
- 大部分 UTF-8 递归压缩包抽取
- sidecar 路径化存储
- PG/ES 层级同步

当前仍需明确标注的限制只有两类：

- `GBK/GB2312 ZIP` 的内层中文文件名乱码
- `UTF-8 中文 RAR` 在当前 Tika 容器下返回 `422`

因此，面向生产的推荐策略是：

- 文档能力默认开启
- 压缩包能力单独开关控制
- 对 ZIP 编码问题使用 Cloudreve 侧编码感知预解包兜底
- 对 RAR 先保持可配置、谨慎开启，再继续做 `unrar` 替换验证
