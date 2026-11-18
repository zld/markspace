# mediacheck

离线媒体容器元数据核验工具，用于音效、配音与过场素材进入引擎前的自检。
支持 WAV（RIFF/WAVE）与 MP4/MOV（ISO BMFF）容器，不依赖 ffmpeg 等外部工具，
不执行解码、不访问网络。

## 安装

```bash
python3.11 -m venv .venv
.venv/bin/python -m pip install -e .
```

## 快速开始

```bash
.venv/bin/python scripts/make_samples.py --out samples
.venv/bin/python -m mediacheck.cli compare samples \
    --db state.sqlite --json report.json --duration-tolerance 0.08
.venv/bin/python -m mediacheck.cli pack samples --out package.json
```

`--duration-tolerance` 以**秒**为单位（`0.08` = 80 ms）。偏差恰等于容差时判定
为一致。

## 命令

| 命令 | 作用 |
| --- | --- |
| `compare <paths...>` | 嗅探、与基线比对、写入 SQLite 并输出摘要 |
| `scan <paths...>` | 只嗅探并记录快照，不做比对 |
| `snapshot <dir>` | 把当前目录状态写成 `baseline.json` |
| `pack <paths...> --out <file>` | 复用同一嗅探器生成打包清单 |

`--db` 可放在子命令前或后；`--json` 写出完整报告（UTF-8，原子替换）。

## 预期状态从哪里来

`compare` 按以下顺序为每个文件解析基线：

1. `--baseline <dir>` 指定的基线目录（按相对路径匹配）；
2. 文件所在目录的 `baseline.json`（样例生成器与 `snapshot` 会写出该文件）；
3. 数据库中该相对路径上一次成功扫描的快照。

因此**首次扫描**没有历史时不会误报，文件改动后的下一次比对照样能定位差异。
损坏的扫描不会覆盖上一次健康的基线。

## 匹配与判定

轨道以 `(类型, 语言, 编码, 稳定序号)` 匹配：

- 轨道重排不产生差异；
- 同一身份出现多条候选时报 `ambiguous`，不会静默择一；
- 字段任一侧缺失记 `unknown_field` / `unknown_duration`，绝不填充默认值；
- 时长偏差、采样参数漂移、编码摘要变化分别报告。

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 扫描完成且无不一致 |
| 1 | 发现不一致或损坏输入 |
| 2 | 命令行参数错误 |
| 3 | 本地存储或输出失败 |

`pack` 拒绝含歧义、截断或结构错误的输入，并在清单的 `rejected` 中给出原因；
存在拒绝项时同样返回 1。

## 安全与限制

- 单个文件最大 2 GiB，box 嵌套深度 16，轨道数 64；
- 只读取输入根目录内的路径，符号链接逃逸会被拒绝并记录；
- JSON 先写同目录临时文件再 `os.replace`，不会留下半截输出。

## 测试

```bash
.venv/bin/python -m unittest discover -s tests
```
