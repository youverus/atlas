package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// dispatch prepares and runs one assistant message's tool calls. It returns the
// tool result messages in assistant source order and whether the batch asked to
// terminate the run.
func (a *Agent) dispatch(ctx context.Context, calls []wire.Content, stopReason wire.StopReason, em *emitter) ([]wire.Message, bool) {
	if stopReason == wire.StopLength {
		return a.dispatchTruncated(ctx, calls, em)
	}
	if a.cfg.ToolExecution == ToolExecutionSequential {
		return a.dispatchSequential(ctx, calls, em)
	}
	return a.dispatchParallel(ctx, calls, em)
}

// dispatchTruncated fails every call when the response hit the output token
// limit, because arguments may be cut off mid-JSON.
func (a *Agent) dispatchTruncated(ctx context.Context, calls []wire.Content, em *emitter) ([]wire.Message, bool) {
	messages := make([]wire.Message, len(calls))
	for i, block := range calls {
		call := tool.Call{ID: block.ID, Name: block.Name, Arguments: block.Arguments}
		em.emit(startEvent(call))
		result := errorResult(fmt.Sprintf(
			"Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the call with complete arguments.",
			call.Name,
		))
		message := a.finalize(ctx, call, result, 0, false)
		messages[i] = message
		emitEnd(em, call, result, 0, false)
		emitResultMessages(em, message)
	}
	return messages, false
}

func (a *Agent) dispatchSequential(ctx context.Context, calls []wire.Content, em *emitter) ([]wire.Message, bool) {
	messages := make([]wire.Message, len(calls))
	terminates := make([]bool, len(calls))

	for i, block := range calls {
		call := tool.Call{ID: block.ID, Name: block.Name, Arguments: block.Arguments}
		em.emit(startEvent(call))

		impl, denial := a.prepare(ctx, call)
		if denial != nil {
			stopped := ctx.Err() != nil
			message := a.finalize(ctx, call, *denial, 0, stopped)
			messages[i] = message
			emitEnd(em, call, *denial, 0, stopped)
			emitResultMessages(em, message)
			continue
		}

		start := time.Now()
		result, stopped := a.execute(ctx, impl, call, em)
		duration := time.Since(start)
		message := a.finalize(ctx, call, result, duration, stopped)
		messages[i] = message
		terminates[i] = result.Terminate
		emitEnd(em, call, result, duration, stopped)
		emitResultMessages(em, message)
	}

	return messages, allTerminate(terminates, len(calls))
}

func (a *Agent) dispatchParallel(ctx context.Context, calls []wire.Content, em *emitter) ([]wire.Message, bool) {
	messages := make([]wire.Message, len(calls))
	terminates := make([]bool, len(calls))

	type job struct {
		index int
		impl  tool.Tool
		call  tool.Call
	}
	var jobs []job

	// Preparation is sequential, matching pi: every start event is emitted in
	// assistant source order before any tool runs.
	for i, block := range calls {
		call := tool.Call{ID: block.ID, Name: block.Name, Arguments: block.Arguments}
		em.emit(startEvent(call))

		impl, denial := a.prepare(ctx, call)
		if denial != nil {
			stopped := ctx.Err() != nil
			message := a.finalize(ctx, call, *denial, 0, stopped)
			messages[i] = message
			emitEnd(em, call, *denial, 0, stopped)
			continue
		}
		jobs = append(jobs, job{index: i, impl: impl, call: call})
	}

	sem := make(chan struct{}, a.cfg.MaxParallel)
	var wg sync.WaitGroup
	for _, entry := range jobs {
		wg.Add(1)
		go func(entry job) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				// The call never started, and it is still an interrupted one:
				// the reader stopped it before it could run, which is the same
				// fact a result that was cut off records.
				result := interruptedResult(nil)
				messages[entry.index] = a.finalize(ctx, entry.call, result, 0, true)
				emitEnd(em, entry.call, result, 0, true)
				return
			}

			start := time.Now()
			result, stopped := a.execute(ctx, entry.impl, entry.call, em)
			duration := time.Since(start)
			messages[entry.index] = a.finalize(ctx, entry.call, result, duration, stopped)
			terminates[entry.index] = result.Terminate
			emitEnd(em, entry.call, result, duration, stopped)
		}(entry)
	}
	wg.Wait()

	// Tool result message artifacts are emitted after the batch, in assistant
	// source order, while end events above followed completion order.
	for _, message := range messages {
		emitResultMessages(em, message)
	}

	return messages, allTerminate(terminates, len(calls))
}

// prepare resolves a call to an executable tool, or returns the error result
// that replaces execution.
func (a *Agent) prepare(ctx context.Context, call tool.Call) (tool.Tool, *tool.Result) {
	if ctx.Err() != nil {
		result := interruptedResult(nil)
		return nil, &result
	}
	impl, ok := a.registry.Get(call.Name)
	if !ok {
		result := errorResult(fmt.Sprintf("Tool %q not found", call.Name))
		return nil, &result
	}
	if a.cfg.BeforeToolCall != nil {
		decision, err := a.cfg.BeforeToolCall(ctx, call)
		if ctx.Err() != nil {
			result := interruptedResult(nil)
			return nil, &result
		}
		if err != nil {
			result := errorResult(err.Error())
			return nil, &result
		}
		if decision != nil && decision.Block {
			reason := decision.Reason
			if reason == "" {
				reason = fmt.Sprintf("Tool call %q was blocked", call.Name)
			}
			result := errorResult(reason)
			return nil, &result
		}
	}
	return impl, nil
}

// execute runs one call and reports whether the caller stopped it.
//
// The partials a tool streams are kept for that second answer: a call that was
// killed halfway through a file is not the same thing as a call that never ran,
// and the only place that difference can be recorded is the result.
func (a *Agent) execute(ctx context.Context, impl tool.Tool, call tool.Call, em *emitter) (tool.Result, bool) {
	if ctx.Err() != nil {
		return interruptedResult(nil), true
	}
	var last *tool.Partial
	result, err := impl.Execute(ctx, call, func(partial tool.Partial) {
		last = &partial
		payload := struct {
			Content []wire.Content  `json:"content"`
			Details json.RawMessage `json:"details,omitempty"`
		}{Content: partial.Content, Details: partial.Details}
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return
		}
		em.emit(wire.AgentEvent{
			Type:          wire.AgentEventToolExecUpdate,
			ToolCallID:    call.ID,
			ToolName:      call.Name,
			Args:          call.Arguments,
			PartialResult: raw,
		})
	})
	if err != nil {
		if ctx.Err() != nil {
			return interruptedResult(last), true
		}
		return errorResult(err.Error()), false
	}
	if result.Content == nil {
		result.Content = []wire.Content{}
	}
	return result, false
}

// interruptedResult reports a call the caller stopped, and what it had produced
// before that. "Operation aborted" alone would leave the reader — and the model
// on the next request — unable to tell a call that wrote half a file from one
// that never started, which is exactly the question an interrupted turn leaves
// behind.
func interruptedResult(last *tool.Partial) tool.Result {
	result := tool.Result{
		Content: []wire.Content{wire.TextContent("Operation aborted")},
		Details: json.RawMessage("{}"),
		IsError: true,
	}
	if last == nil {
		return result
	}
	result.Content = append(result.Content, last.Content...)
	if len(last.Details) > 0 {
		result.Details = last.Details
	}
	return result
}

func (a *Agent) finalize(ctx context.Context, call tool.Call, result tool.Result, duration time.Duration, interrupted bool) wire.Message {
	if a.cfg.AfterToolCall != nil {
		a.cfg.AfterToolCall(ctx, call, &result)
	}
	message := wire.ToolResultMessage(call.ID, call.Name, result.IsError, result.Content...)
	message.Interrupted = interrupted
	if result.Details == nil {
		result.Details = json.RawMessage("{}")
	}
	message.Details = result.Details
	message.StructuredContent = result.StructuredContent
	message.Usage = result.Usage
	if duration > 0 {
		message.DurationMs = wire.Ptr(duration.Milliseconds())
	}
	return message
}

func startEvent(call tool.Call) wire.AgentEvent {
	return wire.AgentEvent{
		Type:       wire.AgentEventToolExecStart,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	}
}

func emitEnd(em *emitter, call tool.Call, result tool.Result, duration time.Duration, stopped bool) {
	event := wire.AgentEvent{
		Type:       wire.AgentEventToolExecEnd,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     resultJSON(result),
		IsError:    wire.Ptr(result.IsError),
	}
	if duration > 0 {
		event.DurationMs = wire.Ptr(duration.Milliseconds())
	}
	if stopped {
		event.Aborted = wire.Ptr(true)
	}
	em.emit(event)
}

func emitResultMessages(em *emitter, message wire.Message) {
	em.emit(messageEvent(wire.AgentEventMessageStart, message))
	em.emit(messageEvent(wire.AgentEventMessageEnd, message))
}

func errorResult(message string) tool.Result {
	return tool.Result{
		Content: []wire.Content{wire.TextContent(message)},
		Details: json.RawMessage("{}"),
		IsError: true,
	}
}

func resultJSON(result tool.Result) json.RawMessage {
	payload := struct {
		Content   []wire.Content  `json:"content"`
		Details   json.RawMessage `json:"details,omitempty"`
		IsError   bool            `json:"isError"`
		Terminate bool            `json:"terminate,omitempty"`
	}{Content: result.Content, Details: result.Details, IsError: result.IsError, Terminate: result.Terminate}
	raw, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

func allTerminate(terminates []bool, total int) bool {
	if total == 0 {
		return false
	}
	for _, terminate := range terminates {
		if !terminate {
			return false
		}
	}
	return true
}
