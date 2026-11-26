# AssetPack

纯本地、纯终端的多轨演出资源打包参考实现。所有媒体样例均由程序确定性生成，不读取外部素材或网络服务。

## 验证

```bash
brew install go@1.23
export PATH="$(brew --prefix go@1.23)/bin:$PATH"

go version
go mod tidy
go build ./...
go test ./...
go build -o "$TMPDIR/assetpack" ./cmd/assetpack

"$TMPDIR/assetpack" demo \
  --out "$TMPDIR/assetpack-demo" \
  --sequence 1 \
  --range 45000:135000 \
  --queue-blocks 2 \
  --slow-consumer-ms 5

shasum -a 256 "$TMPDIR/assetpack-demo/desktop/frame-00012.rgba"
"$TMPDIR/assetpack" verify --root "$TMPDIR/assetpack-demo"
```

一键本地验收：

```bash
scripts/acceptance.sh
scripts/gen-fixtures.sh "$TMPDIR/assetpack-fixtures"
```

`pion/rtp v1.10.6` 的上游 `go.mod` 声明了 `go 1.24.0`；使用 Homebrew Go 1.23.12 时，Go 默认的 `GOTOOLCHAIN=auto` 会自动选择已缓存的兼容工具链完成构建。项目自身代码不使用 Go 1.24 语法。

## 常用命令

```bash
# 第二个序列与坏 mdat
"$TMPDIR/assetpack" demo --out "$TMPDIR/assetpack-demo" --sequence 2
"$TMPDIR/assetpack" demo --out "$TMPDIR/assetpack-demo" --sequence 2 --fault bad-mdat

# 区间读取：[from,to)，超出索引时长返回退出码 2
"$TMPDIR/assetpack" range --root "$TMPDIR/assetpack-demo" --from 180000 --to 180001

# 校验并回滚
"$TMPDIR/assetpack" verify   --root "$TMPDIR/assetpack-demo"
"$TMPDIR/assetpack" rollback --root "$TMPDIR/assetpack-demo" --to previous --verify
```

退出码：

| Code | 含义 |
|---:|---|
| 0 | 成功 |
| 2 | 参数、未知版本/编码、坏输入或区间错误 |
| 3 | 取消（含 SIGINT/SIGTERM 与 `--cancel-after`） |
| 4 | 发布、校验或回滚失败 |

## 内部数据流

```text
samplegen → adapter(mp4/fmp4/rtp/codec v1)
          → clock 90kHz rational mapping
          → timeline.Compile
          → index.Build/Save/Load + range.Reader
          → stream.Queue/Pipeline（默认容量 2，背压与清理）
          → render metrics/hash
          → release.Commit（全量 hash/像素校验后原子切换）
          → release.Rollback
```

关键约束：

- 全局时基为每秒 `90000` tick，映射只使用整数有理数。
- 区间统一为左闭右开 `[From, To)`。
- 磁盘索引记录样本偏移、PTS/Duration 和随机访问前置样本。
- 索引页缓存上限 `4 MiB`，输入解析窗口上限 `1 MiB`。
- 发布先写临时目录，重算 artifact hash、RGB 均值和 PCM 峰值，再原子替换 `current` 与平台符号链接。
- `desktop/`、`mobile/`、`audio/` 是指向当前版本目录的稳定符号链接，因此可直接对 `desktop/frame-00012.rgba` 执行 `shasum`。
