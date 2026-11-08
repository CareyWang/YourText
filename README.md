# YourText

一个基于 Go + S3 兼容对象存储的轻量文本分享服务。提交一段文本，得到一个可访问的链接。默认搭配 [Garage](https://garagehq.deuxfleurs.fr/) 部署，也可以接入任何 S3 兼容存储。

## 特性

- 通过 HTTP 接口上传文本，返回下载链接
- 文本以 `YYYY/MM/DD/<uuid>.txt` 的路径存储在对象存储中
- 单条文本最大 10000 字节
- 默认放开跨域（CORS）
- 提供多架构 Docker 镜像：`careywong/yourtext`

## 快速开始（Docker Compose）

1. 复制并修改环境变量：

   ```bash
   cp .env.example .env
   ```

   必须替换其中的密钥，格式要求如下：

   | 变量 | 格式 | 生成方式 |
   |---|---|---|
   | `YOURTEXT_MINIO_ACCESS_KEY` | `GK` + 24 位十六进制 | `echo "GK$(openssl rand -hex 12)"` |
   | `YOURTEXT_MINIO_SECRET_KEY` | 64 位十六进制 | `openssl rand -hex 32` |
   | `GARAGE_RPC_SECRET` | 64 位十六进制 | `openssl rand -hex 32` |

2. 启动服务：

   ```bash
   docker compose up -d
   ```

`docker-compose.yml` 会同时启动 YourText 和单节点 Garage，并自动创建存储桶和访问密钥。数据保存在 `./data/garage`。

> Garage 单节点没有数据冗余，请定期备份 `./data/garage`。

## 本地运行

需要 Go 1.27+，并且有一个可访问的 S3 兼容存储。可以先用 compose 只启动 Garage（已绑定 `127.0.0.1:3900`）：

```bash
cp .env.example .env              # 替换密钥，YOURTEXT_MINIO_ENDPOINT 保持 localhost:3900
docker compose up -d yourtext-garage
go run .
```

## 配置

通过环境变量配置，也可以写在项目根目录的 `.env` 文件中（文件不存在不影响启动）。

| 变量 | 说明 | 默认值 |
|---|---|---|
| `YOURTEXT_APP_URL` | 对外访问地址，用于拼接返回的链接 | `http://localhost:8080` |
| `YOURTEXT_APP_PORT` | 服务监听端口 | `8080` |
| `YOURTEXT_MINIO_ENDPOINT` | S3 服务地址（`host:port`） | 无 |
| `YOURTEXT_MINIO_ACCESS_KEY` | S3 Access Key | 无 |
| `YOURTEXT_MINIO_SECRET_KEY` | S3 Secret Key | 无 |
| `YOURTEXT_MINIO_BUCKET_NAME` | 存储桶名称，不存在时会自动创建 | 无 |
| `YOURTEXT_S3_REGION` | S3 region，需与存储服务端配置一致（Garage 见 `garage.toml` 的 `s3_region`，默认 `us-east-1`） | `us-east-1` |

说明：`YOURTEXT_MINIO_*` 变量名为兼容旧版本而保留，现在对应任意 S3 兼容存储。服务使用 path-style 寻址，连接存储时不启用 SSL。

## 从 MinIO 迁移存量数据

项目早期使用 MinIO，数据在 `./data/minio`。`YourText sync-minio` 子命令（已内置在镜像中，走 S3 协议）可以把存量数据同步到新存储：只读源、只增不删目标，可重复运行，目标中已存在且大小一致的对象会被跳过。

> MinIO 官方镜像（`minio/minio`）已从 Docker Hub 下架，下面用社区 fork [`pgsty/minio`](https://github.com/pgsty/minio) 临时读取旧数据。该 fork 读取官方 MinIO 写入的数据目录**未经本项目验证**，建议先备份：`cp -a ./data/minio ./data/minio.bak`。

1. **先记下旧 `.env` 中的凭证**（`YOURTEXT_MINIO_ACCESS_KEY`、`YOURTEXT_MINIO_SECRET_KEY`、`YOURTEXT_MINIO_BUCKET_NAME`），后面作为源凭证使用。新版 `.env` 要求 Garage 格式的密钥，改完就找不回旧值了。
2. **停止旧服务**（在更新代码之前，用旧版 compose 文件）：`docker compose down`。数据保留在 `./data/minio`。
3. **更新代码**，按“快速开始”配置新的 `.env`，然后 `docker compose up -d`。Garage 会绑定到 `127.0.0.1:3900`。
4. **临时启动旧 MinIO**，使用旧凭证、挂载旧数据目录，并加入 compose 的网络（这样 `yourtext` 容器可以直接用 `old-minio:9000` 访问它）：

   ```bash
   docker run -d --name old-minio \
     --network "$(docker inspect yourtext -f '{{range $n, $_ := .NetworkSettings.Networks}}{{$n}}{{end}}')" \
     -v "$(pwd)/data/minio:/data" \
     -e MINIO_ROOT_USER=<旧 Access Key> -e MINIO_ROOT_PASSWORD=<旧 Secret Key> \
     pgsty/minio:latest server /data
   ```

5. **同步数据**：在 `yourtext` 容器内执行 `sync-minio` 子命令。目标凭证和存储桶直接取容器里的新配置，只需传入旧凭证：

   ```bash
   export SRC_ACCESS_KEY=<旧 Access Key> SRC_SECRET_KEY=<旧 Secret Key>

   # 先 dry-run，查看待同步的对象数量（--src-bucket 填旧的 YOURTEXT_MINIO_BUCKET_NAME）
   docker compose exec -e SRC_ACCESS_KEY -e SRC_SECRET_KEY yourtext sh -c '
     DST_ACCESS_KEY=$YOURTEXT_MINIO_ACCESS_KEY DST_SECRET_KEY=$YOURTEXT_MINIO_SECRET_KEY \
     ./YourText sync-minio \
       --src-endpoint old-minio:9000 --src-bucket yourtext \
       --dst-endpoint $YOURTEXT_MINIO_ENDPOINT --dst-bucket $YOURTEXT_MINIO_BUCKET_NAME --dry-run'

   # 正式同步：去掉末尾的 --dry-run
   ```

   同步结束时会校验源中每个对象在目标中都存在且大小一致，不一致时以非零状态退出。其他参数见 `./YourText sync-minio -h`。
6. 确认无误后清理：`docker rm -f old-minio`。`./data/minio` 建议再保留一段时间。

因为第 2 步已停止旧服务，同步期间没有新写入，不需要补增量；代价是迁移期间服务不可用（数据量小时只有几秒）。

## API

### 上传文本

`POST /upload`

```bash
curl -X POST http://localhost:8080/upload \
  -H 'Content-Type: application/json' \
  -d '{"content": "hello world"}'
```

成功响应：

```json
{
  "code": 0,
  "msg": "",
  "data": { "url": "http://localhost:8080/2026/11/08/3f2b8c1e-....txt" }
}
```

错误响应：

| HTTP 状态码 | `code` | 说明 |
|---|---|---|
| 400 | 1 | 请求体不合法，或缺少 `content` |
| 400 | 2 | `content` 超过 10000 字节 |
| 500 | 2 | 写入存储失败 |

### 获取文本

`GET /<path>`

访问上传返回的链接即可。响应头带有 `Content-Disposition: attachment`，浏览器会以文件形式下载。

```bash
curl http://localhost:8080/2026/11/08/3f2b8c1e-....txt
```

对象不存在时返回 404。

## 注意

- 服务没有鉴权，任何能访问的人都可以上传和读取文本，请自行在反向代理层做访问控制。
- 链接中的 UUID 是访问文本的唯一凭据，请不要在公开场合泄露。

## License

[MIT](LICENSE)
