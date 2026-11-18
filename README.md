# trackcheck

本地媒体容器元数据抽取与一致性校验工具。纯 Python 3.11 标准库实现，
不调用外部媒体程序、不访问网络。

## 自定义容器格式 `.pwm`

所有整数为大端序：

| 区域 | 内容 |
| --- | --- |
| 文件头 (15B) | 魔数 `PWMC`、版本、头长度、轨道数、声明文件长度 |
| 轨道表 | 每条 56B：轨道 ID、类型、编码、时间基准、起始时间戳、采样数、分辨率/采样率/声道、语言标签、标志 |
| 载荷区 | 按轨道表顺序排列的载荷块 |

解析器不信任任何声明字段：魔数、版本、头长度、轨道表边界和声明文件长度
逐一校验，错误均带文件偏移诊断；文件大小与轨道数有硬上限
（`model.MAX_FILE_BYTES` / `MAX_TRACKS`），防止异常声明触发无界分配。

## 快速开始

```bash
python3.11 -m venv .venv
.venv/bin/python -m pip install pytest==8.3.5
.venv/bin/python scripts/make_samples.py --out .tmp/samples --seed 17
.venv/bin/python -m trackcheck.cli compare \
  --baseline .tmp/samples/base.pwm --input .tmp/samples/drift.pwm \
  --db .tmp/index.sqlite --json .tmp/report.json --tolerance-ms 40
.venv/bin/python -m pytest -q
```

## 子命令

- `scan FILE...`：抽取一个或多个容器的元数据，坏文件不中断批处理。
- `compare --baseline A --input B`：按轨道 ID 比对，报告缺轨、多轨、
  重命名（语言标签）、顺序变化、参数漂移与逐轨时长超差。
- `package --input F --output M.json`：复用同一抽取器做打包前校验
  （重复轨道 ID、零时间基准、非法时间戳、必需轨道），全部通过后原子写入
  清单，失败时不留半成品。

`--db` 启用 sqlite 快照缓存（路径 + SHA-256 内容哈希），未变化文件直接
复用；`--rebuild` 显式重建损坏的数据库。

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 成功且无超差 |
| 1 | 检测到不一致 |
| 2 | 参数或输入错误（含不可读/损坏容器） |
| 3 | 存储或报告写入失败 |

## 项目布局

```
src/trackcheck/   model probe consistency package store report cli
scripts/          make_samples.py 本地确定性样例生成（固定随机种子）
tests/            端到端流程与错误链路测试
trackcheck/       根级包 shim，支持未安装时 python -m trackcheck.cli
```
