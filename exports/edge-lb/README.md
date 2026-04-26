# edge-lb 镜像导出

文件说明：
- `swarm-edge-lb-20260425-4.tar.xz`：包含以下两个 tag
  - `cloudreve/swarm-edge-lb:20260425-4`
  - `10.37.129.11:15000/cloudreve/swarm-edge-lb:20260425-4`

加载方式：
```bash
docker load -i exports/edge-lb/swarm-edge-lb-20260425-4.tar.xz
```

如果 `docker load` 不支持直接读取 `.xz`，先解压：
```bash
xz -dk exports/edge-lb/swarm-edge-lb-20260425-4.tar.xz
docker load -i exports/edge-lb/swarm-edge-lb-20260425-4.tar
```
