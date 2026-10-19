# 智能解压（fnOS）

面向飞牛 fnOS 的下载目录智能解压应用。自动发现压缩包、识别加密、匹配密码、处理二次压缩与分卷，并通过 Web 界面管理任务。

当前版本：**1.3.1**

## 功能

- **自动发现**：递归扫描监控目录，按文件头识别压缩包，扩展名被改掉也能识别。
- **下载完成校验**：文件大小稳定且没有进程继续写入后才开始解压，避免处理未下载完的文件。
- **百度网盘目录**：内置 `/vol1/1000/baidu` 等常见目录识别，自动跳过 `.baiduyun.*.downloading` 临时文件。
- **只处理新下载**：默认跳过监控目录中的历史文件，避免一次性解压大量旧资源；可随时关闭或重置起点。
- **密码库**：
  - 手动维护密码列表，按顺序优先尝试
  - 从 txt 文件导入（UTF-8/GBK，`#` 开头为注释）
  - 自动记录匹配成功的密码
  - 新增密码并保存或导入后，自动重试等待密码及密码错误的任务
- **嵌套解压**：解压后继续发现并处理内部压缩包，默认最多 3 层。
- **分卷支持**：
  - `.7z.001`、`.7z.002` ...
  - `.zip.001`、`.zip.002` ...
  - `.part1.rar`、`.part2.rar` ...
  - `.rar` + `.r00`、`.r01` ...
  - `.zip` + `.z01` ...
  - 分卷不全时显示「待分卷」，后续分卷到齐后自动继续
- **Web 管理**：任务队列、进度、日志、手动补录密码、重试、目录浏览、设置管理。
- **仅解压不清理**：不删除原压缩包，也不修改文件名中的广告文字。

支持的压缩格式：7z、ZIP、RAR、TAR、GZIP、BZIP2、XZ 及其嵌套组合。RAR 解压由内置 7-Zip 完成。

## 使用

安装完成后可通过以下方式打开：

- 飞牛桌面图标「智能解压」
- `http://<NAS-IP>:18687`

首次使用：

1. 在「监控目录」中添加下载目录，或点击「添加百度网盘目录」自动识别。
2. 按需调整扫描间隔、文件稳定时间、嵌套层数和「只处理新下载」。
3. 在密码库中粘贴常用密码，或导入 NAS 上的密码 txt 文件。
4. 点击「立即扫描」。

任务状态：

| 状态 | 含义 |
| --- | --- |
| 排队中 | 已加入队列 |
| 识别中 | 正在读取压缩包信息 |
| 待密码 | 自动匹配失败，需要补录密码 |
| 待分卷 | 分卷不完整，等待后续分卷 |
| 解压中 | 正在解压 |
| 已完成 | 解压完成 |
| 失败 | 解压失败，可查看日志后重试 |
| 已跳过 | 不是可处理的压缩包 |

## 密码匹配顺序

1. 文件名、压缩包注释、同目录 txt/nfo 中的明确密码提示
2. 手动密码库
3. 已学习密码
4. 同目录文本、文件名中的域名和候选词
5. 内置常见密码

新增密码后，所有「待密码」任务以及因密码错误失败的任务会自动重新尝试。

## 安装

### FPK 安装包

安装包位于 `dist/com.smartunzip.app-<version>.fpk`，可在飞牛应用中心手动安装，或使用：

```bash
trim-cli app install-fpk com.smartunzip.app-1.3.1.fpk --volume-id <volumeId> --yes
```

默认端口 `18687`，安装后自动创建桌面入口。应用以 root 身份运行，以便读取各存储卷中的下载目录。

> 本地 FPK 覆盖安装可能被应用中心拒绝（错误码 `10236`）。可先卸载旧版本再安装，数据目录会保留。

### 数据目录

运行时数据位于 fnOS 应用数据目录，通常为：

```text
/vol<n>/@appdata/com.smartunzip.app/
├── state.json      # 任务、设置、已学习密码、已处理记录
├── passwords.txt   # 密码库
├── app.pid
└── info.log
```

卸载应用时 fnOS 会保留数据目录。

## 从源码构建

环境要求：

- Go 1.24+
- Python 3
- Windows PowerShell（当前构建脚本）
- fnpack 1.2.3
- 7-Zip 25.01 Linux amd64 / arm64 二进制

`tools/` 已被 `.gitignore` 忽略，构建前需要准备：

```text
tools/
├── fnpack.exe
├── 7z-x64/
│   ├── 7zz
│   └── License.txt
└── 7z-arm64/
    └── 7zz
```

下载地址：

- fnpack：<https://developer.fnnas.com/docs/cli/fnpack>
- 7-Zip：<https://www.7-zip.org/download.html>

执行构建：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build.ps1
```

构建流程：

1. 从 `fpk/manifest` 读取版本号
2. 交叉编译 Linux amd64 / arm64 程序
3. 复制 7-Zip 引擎
4. 调用 `fnpack build`
5. 使用 `scripts/fix_fpk.py` 修正包内可执行权限和 checksum
6. 输出到 `dist/com.smartunzip.app-<version>.fpk`

本地调试运行：

```bash
go run . -addr 127.0.0.1:18687 -data-dir ./data -7zz /path/to/7zz
```

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/state` | 设置、任务、统计、引擎状态 |
| GET | `/api/health` | 健康检查 |
| GET | `/api/browse?path=` | 浏览 NAS 目录 |
| GET | `/api/baidu-dirs` | 检测常见百度网盘下载目录 |
| POST | `/api/settings` | 保存设置 |
| POST | `/api/scan` | 立即扫描 |
| POST | `/api/passwords/import` | 从 txt 导入密码 |
| POST | `/api/baseline/reset` | 重置「只处理新下载」起点 |
| POST | `/api/tasks/{id}/password` | 提交手动密码 |
| POST | `/api/tasks/{id}/retry` | 重试任务 |
| POST | `/api/learned/delete` | 删除一条已学习密码 |
| POST | `/api/learned/clear` | 清空已学习密码 |
| DELETE | `/api/tasks/{id}` | 移除任务记录 |

## 项目结构

```text
.
├── main.go              # 启动入口
├── state.go             # 设置、任务与持久化
├── engine.go            # 扫描、队列与解压流程
├── archive.go           # 压缩包识别、7-Zip 调用、密码测试
├── volumes.go           # 分卷识别与完整性检查
├── passwords.go         # 密码候选生成与密码文件解析
├── completion.go        # 下载完成判断、百度目录检测
├── server.go            # HTTP API
├── web/index.html       # 嵌入式 Web 界面
├── fpk/                 # FPK 应用包目录
├── scripts/build.ps1    # 构建脚本
├── scripts/fix_fpk.py   # FPK 权限与 checksum 修正
└── .gitignore
```

## 更新日志

- **1.3.1**：密码库新增密码并保存或导入后，自动重试等待密码及密码错误的任务。
- **1.3.0**：支持 `.7z.001`、`.zip.001`、`.part1.rar`、`.rar + .r00` 等分卷，分卷不完整时自动等待。
- **1.2.1**：优化大目录扫描，只处理新下载时提前跳过历史文件。
- **1.2.0**：新增「只处理新下载」起点控制、百度网盘目录检测、密码文件导入与已学习密码管理。
- **1.1.0**：新增百度网盘下载完成校验、密码库文件导入。
- **1.0.0**：首次发布，支持自动识别、密码匹配、嵌套解压和 Web 管理。

## 注意事项

- Web 界面没有登录认证，请仅在可信局域网内使用，不要直接暴露到公网。
- 应用以 root 运行，可读取和写入所有存储卷；请仅安装在自己的设备上。
- 默认只处理新下载。关闭「只处理新下载」后，扫描会处理监控目录中已有的全部压缩包，请先确认磁盘空间。
- 解压输出默认放在压缩包同级目录；不删除原压缩包。
- 分卷解压需要所有分卷位于同一目录，且文件名符合常见分卷规则。
