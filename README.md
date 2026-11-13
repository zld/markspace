# forensic-timeline

离线取证时间线重建工具：把自有样本镜像中的事件记录、镜像采集边界和清单校验
放到同一条 UTC 时间线上，量化时钟漂移、完整性不一致与缺失时间段。

仅处理**本工具生成的本地样本**（清单带 `forensic-timeline-sample/1` 格式标记），
镜像一律只读打开，拒绝设备、分区及任何外部证据源。纯 Python 3.12 标准库，
无第三方依赖，支持 Linux / macOS / Windows。

## 运行

```sh
# 方式一：可编辑安装后直接使用（或安装后运行 forensic-timeline 命令）
pip install -e .
python -m forensic_timeline.cli --self-check --timezone Asia/Shanghai \
    --drift-limit-seconds 180 --gap-threshold-seconds 900 \
    --db /tmp/forensic-timeline.sqlite

# 方式二：不安装，直接指定源码路径
PYTHONPATH=src python -m forensic_timeline.cli --self-check ...
```

自检模式在临时目录生成样本（12 个有效事件、1 条坏记录、1 条重复事件、
2 个被篡改块、+37s 时钟漂移、1 处 1800s 采集间断），随后完整跑通
读取 → 时钟校正 → 完整性校验 → 时间线 → SQLite 持久化 → 报告流程。

也可用 `--image/--manifest` 分析此前生成的样本对；`--json <路径>` 额外输出
机器可读报告。

## 测试

```sh
PYTHONPATH=src python -m unittest discover -s tests -v
```

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 分析完成（完整性差异属于分析结果，不算失败） |
| 1  | 未预期的内部错误 |
| 2  | 命令行 / 配置错误（时区、阈值越界等） |
| 3  | 镜像或清单不可读、非法或非自有样本 |
| 4  | 数据库被锁定 |
| 5  | 磁盘写满 / 输出写入失败 |

## 设计要点

- **时钟**：带偏移时间直接使用；裸时间按 `--timezone` 指定的 IANA 时区解释，
  夏令时歧义 / 不存在时间明确标记并隔离，绝不静默猜测。漂移由清单锚点的
  中位数估计，超过 `--drift-limit-seconds` 或锚点不足时保留原始时间并标注
  低置信度，不做隐式校正。在缺少 IANA 时区库的环境（典型为未安装 `tzdata`
  的精简 Windows Python）上，自动回退到内置规则时区表（覆盖
  `Asia/Shanghai`、`UTC`、`America/New_York`、`Europe/Berlin`），不引入
  第三方依赖；规则时区的 DST 歧义/不存在时间语义与 `zoneinfo` 完全一致。
- **完整性**：分块 SHA-256 与清单逐项比对；哈希不一致、清单缺块、镜像多余块、
  哈希格式错误分别报告，互不合并。镜像被截断导致块缺失时报告 `missing_block`
  而非崩溃。
- **时间线**：按校正后 UTC 稳定排序，重复事件保留来源并标记；缺失区间按
  可配置阈值推断，首尾区间以清单采集起止边界计算。
- **持久化**：每个批次（输入摘要、配置、事件、异常、校验结果、缺口）在单个
  SQLite 事务中写入，失败整体回滚；批次使用唯一 ID，重复运行不覆盖历史。
  事件同时保留原始时间戳、原始时区、是否裸时间（`naive`）以及校正前后的 UTC；
  旧版数据库在连接时自动补齐 `naive` 列。
