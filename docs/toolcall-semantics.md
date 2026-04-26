# Tool call parsing semantics（Go/Node 统一语义）

本文档描述当前代码中的**实际行为**，以 `internal/toolcall`、`internal/toolstream` 与 `internal/js/helpers/stream-tool-sieve` 为准。

文档导航：[总览](../README.MD) / [架构说明](./ARCHITECTURE.md) / [测试指南](./TESTING.md)

## 1) 当前唯一可执行格式

当前版本只把下面这类 canonical XML 视为可执行工具调用：

```xml
<tool_calls>
  <invoke name="read_file">
    <parameter name="path"><![CDATA[README.MD]]></parameter>
  </invoke>
</tool_calls>
```

约束：

- 必须有 `<tool_calls>...</tool_calls>` wrapper
- 每个调用必须在 `<invoke name="...">...</invoke>` 内
- 工具名必须放在 `invoke` 的 `name` 属性
- 参数必须使用 `<parameter name="...">...</parameter>`

兼容修复：

- 如果模型漏掉 opening `<tool_calls>`，但后面仍输出了一个或多个 `<invoke ...>` 并以 `</tool_calls>` 收尾，Go 解析链路会在解析前补回缺失的 opening wrapper。
- 这是一个针对常见模型失误的窄修复，不改变推荐输出格式；prompt 仍要求模型直接输出完整 canonical XML。

## 2) 非 canonical 内容

任何不满足上述 canonical XML 形态的内容，都会保留为普通文本，不会执行。一个例外是上一节提到的“缺失 opening `<tool_calls>`、但 closing `</tool_calls>` 仍存在”的窄修复场景。

当前 parser 不把 allow-list 当作硬安全边界：即使传入了已声明工具名列表，XML 里出现未声明工具名时也会尽量解析并交给上层协议输出；真正的执行侧仍必须自行校验工具名和参数。

## 3) 流式与防泄漏行为

在流式链路中（Go / Node 一致）：

- canonical `<tool_calls>` wrapper 会进入结构化捕获
- 如果流里直接从 `<invoke ...>` 开始，但后面补上了 `</tool_calls>`，Go 流式筛分也会按缺失 opening wrapper 的修复路径尝试恢复
- 已识别成功的工具调用不会再次回流到普通文本
- 不符合新格式的块不会执行，并继续按原样文本透传
- fenced code block 中的 XML 示例始终按普通文本处理

## 4) 输出结构

`ParseToolCallsDetailed` / `parseToolCallsDetailed` 返回：

- `calls`：解析出的工具调用列表（`name` + `input`）
- `sawToolCallSyntax`：检测到 canonical wrapper，或命中“缺失 opening wrapper 但可修复”的形态时会为 `true`
- `rejectedByPolicy`：当前固定为 `false`
- `rejectedToolNames`：当前固定为空数组

## 5) 落地建议

1. Prompt 里只示范 canonical XML 语法。
2. 上游客户端仍应直接输出 canonical XML；DS2API 只对“closing tag 在、opening tag 漏掉”的常见失误做窄修复，不会泛化接受其他旧格式。
3. 不要依赖 parser 做安全控制；执行器侧仍应做工具名和参数校验。

## 6) 回归验证

可直接运行：

```bash
go test -v -run 'TestParseToolCalls|TestProcessToolSieve' ./internal/toolcall ./internal/toolstream ./internal/httpapi/openai/...
node --test tests/node/stream-tool-sieve.test.js
```

重点覆盖：

- canonical `<tool_calls>` wrapper 正常解析
- 非 canonical 内容按普通文本透传
- 代码块示例不执行
