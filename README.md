# sidx_tool

纯本地、零第三方运行时依赖的 BMFF（ISO/IEC 14496-12）分片索引重建与样本表校验工具。
用于把外部制作的剧情演出视频纳入可复现的资产构建流程：修复过期 `sidx`、移动 `mdat`
后失效的 `stco`/`co64` 偏移，以及互相矛盾的 `stsc`/`stsz`/`stts` 记录。

## 适用范围

- 未加密、非分片式（classic）MP4；不解码音视频、不联网、不调用任何外部服务。
- 仅使用 Python 3.11 标准库（`struct`、`pathlib`、`tempfile`、`fractions` 等）。
- 终端形态，无图形界面、无浏览器/摄像头/麦克风依赖。

## 安装

```bash
brew install python@3.11
python3.11 -m venv .venv
.venv/bin/python -m pip install 'pytest>=8.3,<9'   # 测试依赖仅此一项
```

## 命令

### 1. 生成确定性夹具

```bash
.venv/bin/python -m sidx_tool fixture \
  --out .tmp/source.mp4 --seed 41 \
  --samples-per-track 60 --sample-bytes 1000 \
  --multi-mdat --index-state stale
```

`--index-state` 可选 `fresh` / `stale`（默认，引用大小故意写错）/ `missing` / `damaged`。
相同参数永远产出字节一致的文件。

### 2. 重建索引并重排媒体

```bash
.venv/bin/python -m sidx_tool rebuild \
  --input .tmp/source.mp4 --output .tmp/rebuilt.mp4 \
  --segment-ms 500 --max-drift-ms 80
```

- 视频分片边界落在同步样本（SAP）上；音轨按解码时间归入对应分片。
- 边界漂移超过 `--max-drift-ms` 时拒绝产出（退出码 3），绝不静默对齐。
- 分块偏移超过 32 位时自动把 `stco` 提升为 `co64`，并重算所有父框长度。
- 写入同目录临时文件，独立复读校验通过后才 `os.replace` 原子替换目标。

### 3. 独立复读校验

```bash
.venv/bin/python -m sidx_tool verify --input .tmp/rebuilt.mp4 --json
```

`--json` 为每个分片打印一行 JSON，字段固定为
`start_ms`、`end_ms`、`start_offset`、`end_offset`。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 成功 |
| 2  | 容器格式损坏或使用不支持的特性 |
| 3  | 重建结果交叉校验不一致（含漂移超容差） |
| 4  | 本地文件读写失败 |

## 模块布局

| 模块 | 职责 |
|------|------|
| `boxes.py` | 有边界检查的 BMFF 框解析：嵌套、32/64 位长度、未知框跳过、深度/数量上限 |
| `sample_tables.py` | 解析并展开 stts/ctts/stsc/stsz/stz2/stco/co64/stss，校验样本数与 mdat 覆盖 |
| `sidx.py` | SIDX v0/v1 解析、校验与序列化 |
| `layout.py` | 精确（`Fraction`）样本时间、字节范围、轨道映射与分片规划 |
| `moov_writer.py` | 重写 stsc/stco/co64，其余叶子框逐字节保留 |
| `rebuild.py` | 两遍偏移预算、流式重排、临时文件 + 原子替换、统一重定位补偿 |
| `validate.py` | 独立复读输出并交叉校验引用边界、样本范围、分片无遗漏无重叠 |
| `fixture.py` | 确定性多 mdat 夹具与过期/缺失/损坏索引变体 |
| `cli.py` / `__main__.py` | 命令行入口 |

## 测试

```bash
.venv/bin/python -m pytest -q
```

## 安全约束

- 框嵌套深度、框数量、样本数量、单框大小均有硬上限（见 `__init__.py`）。
- 拒绝负长度、溢出偏移、样本区间重叠；媒体拷贝使用 64 KiB 有界缓冲。
- 任何失败路径都清理临时文件，绝不留下半成品输出。
- 临时产物统一放在 `.tmp/`，不纳入源码交付。
