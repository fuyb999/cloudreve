将需要额外加载的字体文件放到此目录，`docker-compose` 默认会将其只读挂载到容器内的 `/tika-fonts/custom`。

支持常见字体格式：

- `.ttf`
- `.ttc`
- `.otf`
- `.otc`

容器启动时会自动刷新 `fontconfig` 缓存，使这些字体可被 Tika / PDFBox / POI 使用。

当前镜像还会把 `/tika-fonts/custom:/tmp/tika-font-links` 注入 JVM 的 `sun.java2d.fontpath`，减少 PDF / Office 解析时因 Java 侧找不到字体导致的缺字问题。

## 使用外部宿主机字体目录

默认挂载来源可通过 `docker-compose.yml` 中的环境变量覆盖：

```bash
TIKA_CUSTOM_FONTS_MOUNT_TYPE=bind
TIKA_CUSTOM_FONTS_MOUNT_SOURCE=/data/fonts/chinese
docker-compose up -d --build tika
```

上面的宿主机目录会被挂载到容器内的 `/tika-fonts/custom`。

如果你还在用旧变量 `TIKA_CUSTOM_FONTS_HOST_PATH`，Swarm 准备脚本和部署脚本目前仍然兼容，但新配置建议统一改成 `TIKA_CUSTOM_FONTS_MOUNT_TYPE + TIKA_CUSTOM_FONTS_MOUNT_SOURCE`。

适合放进去的中文字体包括：

- `PingFang`
- `Microsoft YaHei` / `微软雅黑`
- `SimSun` / `宋体`
- `SimHei` / `黑体`
- `Source Han Sans/Serif` / `思源黑体/宋体`

如果你需要挂载其他目录，也可以在运行容器时设置：

```bash
TIKA_FONT_DIRS=/tika-fonts/custom:/another/fonts
```

并把对应宿主机目录挂载到容器内路径，例如：

```yaml
services:
  tika:
    environment:
      - TIKA_FONT_DIRS=/tika-fonts/custom:/tika-fonts/company
    volumes:
      - ./docker/tika-unrar/fonts:/tika-fonts/custom:ro
      - /data/company-fonts:/tika-fonts/company:ro
```

如需排查字体是否已被容器识别，可临时开启：

```bash
TIKA_FONT_DEBUG=1
```

容器启动日志里会打印关键中文字体命中情况。
