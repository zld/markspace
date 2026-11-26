# continuity

纯本地、无外部服务的 HLS/DASH 主备源连续性复核工具。所有样例均由
`internal/fixture` 用代码生成，不读取仓库外媒体文件。

## 验收命令

在项目目录执行：

```bash
go mod tidy
go build -o /tmp/continuity ./cmd/continuity
go test ./...

/tmp/continuity verify --case clean --out /tmp/continuity-clean
/tmp/continuity verify --case damaged
/tmp/continuity verify --case boundary
/tmp/continuity verify --case recovery --out /tmp/continuity-recovery
```

也可运行完整脚本：

```bash
./scripts/acceptance.sh
```

## 退出码约定

| 场景 | 退出码 |
| --- | ---: |
| `verify` 正确识别全部预期故障 | `0` |
| 直接运行 `run` 遇到路径越界、重复引用、非法时长、截断 MP4、时间溢出等输入违规 | `2` |
| 指定 `--inject-crash`，产物和校验摘要已落盘、状态未提交 | `75` |
| 哈希不符、验收断言失败或其他验收失败 | `1`（哈希冲突内部码为 `65`） |

## 子系统

- `internal/manifest`：HLS master/media playlist 与静态 DASH 解析；路径沙箱、重复引用和时长校验。
- `internal/media`：整数纳秒时基、模数回绕、溢出/歧义错误模型；基于 `go-mp4` 读取 fMP4 `mfhd/tfhd/tfdt/trun/mdat`。
- `internal/rtp`：基于 `pion/rtp` 解析记录，序号窗口展开、重排、去重和 XOR 单包恢复。
- `internal/timeline`：按 PTS 对齐音视频，记录切源边界、同步误差和缺失轨道；缺失只标记不填补。
- `internal/render`：标准库 `image/png` 离屏生成确定性复核帧，编码轨道、PTS 二进制条、切源和缺口标记。
- `internal/journal`：临时文件哈希校验后原子改名，先写帧与 `SHA256SUMS`，再原子提交 `state.json` 和同步日志。
- `internal/migrate`：v1/v2 版本适配器，读取校验、影子写入、原子切换和 `.checkpoint-v1` 检查点回滚。
- `internal/fixture`：正常、损坏、边界和恢复样例。

## 输出

正常场景在 `--out` 下生成：

- `frames/0001.png` ... `frames/0008.png`
- `SHA256SUMS`
- `state.json`：帧序号、文件哈希、PTS 和输入来源
- `journal.log`：准备、校验和提交记录
- `sample/`：代码生成的 HLS/DASH、fMP4 和 RTP 输入

相同输入和相同输出目录重跑会复用并校验已有帧；崩溃后的第二次运行补记状态，不产生重复发布。若已有文件内容与期望哈希不符，工具立即停止且不覆盖现场。
