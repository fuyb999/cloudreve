# ARM64 构建说明

本文说明如何在 x86 Linux 主机上构建 `tika-unrar` 的 ARM64 镜像。

## 前置条件

- Docker Engine 支持 Buildx
- 主机可以访问 Docker 镜像仓库
- x86 主机已启用 ARM64 的 QEMU/binfmt 仿真

启用 ARM64 仿真：

```bash
docker run --privileged --rm tonistiigi/binfmt:latest --install arm64
```

检查 builder 是否支持 ARM64：

```bash
docker buildx inspect --bootstrap
```

输出的 `Platforms` 中应包含：

```text
linux/arm64
```

如果当前 builder 没有 ARM64，可以创建一个使用容器驱动的 builder：

```bash
docker buildx create \
  --name cloudreve-arm64 \
  --driver docker-container \
  --use

docker buildx inspect --bootstrap
```

## 构建镜像

必须在 Cloudreve 仓库根目录执行构建，不能把 `docker/tika-unrar` 作为构建上下文。Dockerfile 中的 `COPY` 路径是相对于仓库根目录的。

```bash
docker buildx build \
  --platform linux/arm64 \
  --progress=plain \
  -f docker/tika-unrar/Dockerfile \
  -t cloudreve/tika:3.2.3.0-full-unrar-charset-arm64 \
  --load \
  .
```

`--load` 会将单平台镜像加载到本机 Docker。若要直接推送到镜像仓库，改用 `--push`：

```bash
docker buildx build \
  --platform linux/arm64 \
  -f docker/tika-unrar/Dockerfile \
  -t <registry>/<namespace>/tika:3.2.3.0-full-unrar-charset-arm64 \
  --push \
  .
```

## 验证镜像

查看镜像架构：

```bash
docker image inspect \
  cloudreve/tika:3.2.3.0-full-unrar-charset-arm64 \
  --format 'architecture={{.Architecture}} os={{.Os}}'
```

预期输出：

```text
architecture=arm64 os=linux
```

验证 ARM64 容器中的 Java 和 `unrar-free`：

```bash
docker run --rm \
  --platform linux/arm64 \
  --entrypoint /bin/sh \
  cloudreve/tika:3.2.3.0-full-unrar-charset-arm64 \
  -c 'uname -m; dpkg --print-architecture; readlink -f /usr/local/bin/unrar; java -version'
```

在 x86 主机上直接运行 ARM64 容器时，Docker 会显示平台不匹配警告，这是使用 QEMU 仿真运行的正常提示。

