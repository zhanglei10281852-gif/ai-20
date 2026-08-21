# Bug Reproduction

## 包的性质

当前 test_model_fix 保存的是被测模型修复后的结果源码，不是初始含 Bug 源码。要复现原始缺陷，必须检出下面固定的 parent SHA；不要在当前修复结果源码上期待重新出现修复前失败。生成系统使用的可信验证补丁和完整验证日志仅在本地留存，不提交到结果分支。

## 问题现象

排期运营通过 `from/to` 查询计划开始时间，一条在窗口内启动、预计结束落在窗口外的长任务却没有返回；这个接口约定只筛计划开始时刻。请修复窗口过滤，让上下界使用同一业务时间维度，并保留工作空间和状态等条件的原有语义。当前接口用例请原样保留，不要修改测试文件或断言，也别跳过验证或换成更弱的检查。

## 含 Bug 版本

- 仓库：zhanglei10281852-gif/ai-20
- 仓库地址：https://github.com/zhanglei10281852-gif/ai-20.git
- parent SHA：b1e0bb9a024c74cb000bf33ac6fac1f68612bbe0

## 复现步骤

```bash
git clone -- https://github.com/zhanglei10281852-gif/ai-20.git bug-repro
cd bug-repro
git checkout --detach b1e0bb9a024c74cb000bf33ac6fac1f68612bbe0
go test ./internal/storage/sqlite -run ^TestInferenceRunWindowFiltersScheduledStartOnly$ -count=1
```

## 双架构完整错误信息

### linux/amd64

- 容器内复现预期退出码：1
- 容器内复现实际退出码：1

stdout：

```text
$ go test ./internal/storage/sqlite -run ^TestInferenceRunWindowFiltersScheduledStartOnly$ -count=1
--- FAIL: TestInferenceRunWindowFiltersScheduledStartOnly (0.09s)
    annotation_repo_behavior_test.go:169: scheduled-start window page = {Items:[] Total:0}
FAIL
FAIL	github.com/zhanglei10281852-gif/ai/internal/storage/sqlite	0.098s
FAIL

```

stderr：

```text
(empty)
```

### linux/arm64

- 容器内复现预期退出码：1
- 容器内复现实际退出码：1

stdout：

```text
$ go test ./internal/storage/sqlite -run ^TestInferenceRunWindowFiltersScheduledStartOnly$ -count=1
--- FAIL: TestInferenceRunWindowFiltersScheduledStartOnly (0.49s)
    annotation_repo_behavior_test.go:169: scheduled-start window page = {Items:[] Total:0}
FAIL
FAIL	github.com/zhanglei10281852-gif/ai/internal/storage/sqlite	0.848s
FAIL

```

stderr：

```text
(empty)
```

## 通过条件

from/to 窗口的上下界必须统一筛选 scheduled start；计划开始位于窗口内时，即使 expected finish 超出窗口也应返回。工作空间、状态等其他条件继续按原语义叠加。定向存储用例和相关查询回归须通过，测试文件与断言不得改动或弱化。
