// Command openai runs bounded, live Responses API descriptor probes.
// It writes response bodies with opaque values hashed; it never writes headers.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
)

type object = map[string]any

var out = flag.String("out", "testdata/openai", "sanitized fixture directory")
var section = flag.String("section", "all", "probe section: all, reasoning, cache, or compaction")

func main() {
	flag.Parse()
	if os.Getenv("OPENAI_"+"API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "OpenAI credential is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	client := openai.NewClient()
	p := &probe{client: &client, ctx: ctx}
	p.models()
	for _, model := range []string{"gpt-6-astra", "gpt-6-luna"} {
		if *section == "all" || *section == "reasoning" {
			p.reasoning(model)
		}
		if *section == "all" || *section == "cache" {
			p.cache(model)
		}
		if *section == "all" || *section == "compaction" {
			p.compaction(model)
		}
	}
	if err := write("index", object{"sdk": "github.com/openai/openai-go/v3 v3.66.0", "observations": p.notes}); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %d observations to %s\n", len(p.notes), *out)
}

type probe struct {
	client *openai.Client
	ctx    context.Context
	notes  []object
}

func (p *probe) call(name, path string, req object) object {
	var body object
	err := p.client.Post(p.ctx, path, req, &body)
	note := object{"name": name, "endpoint": path, "request": shape(req)}
	if err != nil {
		note["error"] = scrubError(err.Error())
		fmt.Printf("%s: error: %s\n", name, scrubError(err.Error()))
	} else {
		note["response"] = sanitize(body)
		fmt.Printf("%s: input=%v cached=%v output=%v types=%v\n", name, at(body, "usage", "input_tokens"), at(body, "usage", "input_tokens_details", "cached_tokens"), at(body, "usage", "output_tokens"), outputTypes(body))
	}
	p.notes = append(p.notes, note)
	if err := write(name, note); err != nil {
		panic(err)
	}
	return body
}

func (p *probe) models() {
	var body object
	err := p.client.Get(p.ctx, "/models", nil, &body)
	note := object{"name": "models", "endpoint": "/models"}
	if err != nil {
		note["error"] = scrubError(err.Error())
	} else {
		ids := []string{}
		for _, v := range array(body["data"]) {
			if id, ok := asObj(v)["id"].(string); ok {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		note["model_ids"] = ids
	}
	p.notes = append(p.notes, note)
	if err := write("models", note); err != nil {
		panic(err)
	}
	if ids, ok := note["model_ids"].([]string); ok {
		fmt.Printf("models: %d available model IDs\n", len(ids))
	}
}

func (p *probe) reasoning(model string) {
	base := strings.ReplaceAll(model, "-", "_")
	tool := object{"type": "function", "name": "lookup", "description": "Return the secret word for a code", "parameters": object{"type": "object", "properties": object{"code": object{"type": "string"}}, "required": []string{"code"}, "additionalProperties": false}, "strict": true}
	first := object{"model": model, "store": false, "max_output_tokens": 512, "reasoning": object{"effort": "high"}, "input": []any{object{"role": "user", "content": "Compute 673 times 887 modulo 97. Call lookup with code A if the remainder is even, otherwise code B. After the tool result, answer with its word."}}, "tools": []any{tool}, "tool_choice": object{"type": "function", "name": "lookup"}}
	r1 := p.call(base+"_r1_tool", "/responses", first)
	items := array(r1["output"])
	var call object
	for _, v := range items {
		if asObj(v)["type"] == "function_call" {
			call = asObj(v)
			break
		}
	}
	if call == nil {
		return
	}
	result := object{"type": "function_call_output", "call_id": call["call_id"], "output": "A = cobalt"}
	history := []any{first["input"].([]any)[0]}
	history = append(history, items...)
	history = append(history, result)
	cont := func(name string, input []any, extra object) object {
		req := object{"model": model, "store": false, "max_output_tokens": 160, "reasoning": object{"effort": "medium"}, "input": input}
		for k, v := range extra {
			req[k] = v
		}
		return p.call(base+"_"+name, "/responses", req)
	}
	cont("r1_replay", history, nil)
	noReason := []any{history[0]}
	for _, v := range history[1:] {
		if asObj(v)["type"] != "reasoning" {
			noReason = append(noReason, v)
		}
	}
	cont("r2_drop", noReason, nil)
	modified := clone(history)
	for _, v := range modified {
		item := asObj(v)
		if item["type"] == "reasoning" {
			if s, ok := item["encrypted_content"].(string); ok && len(s) > 2 {
				item["encrypted_content"] = "X" + s[1:]
				break
			}
		}
	}
	cont("r3_corrupt", modified, nil)
	changed := clone(history)
	asObj(changed[0])["content"] = "Use lookup with code B, then answer with the word returned."
	cont("r4_edit_earlier", changed, nil)
	// A new user turn separates the tool reasoning from the next inference.
	withOlder := append(clone(history), object{"role": "user", "content": "What was the lookup word? One word."})
	cont("r5_keep_older", withOlder, nil)
	withoutOlder := []any{}
	for _, v := range withOlder {
		if asObj(v)["type"] != "reasoning" {
			withoutOlder = append(withoutOlder, v)
		}
	}
	cont("r5_drop_older", withoutOlder, nil)
	// Stateful chaining is deliberately separate from store=false.
	state := p.call(base+"_state_first", "/responses", object{"model": model, "store": true, "max_output_tokens": 80, "input": "Say cobalt."})
	if id, ok := state["id"].(string); ok {
		cont("state_previous", []any{object{"role": "user", "content": "Repeat the word."}}, object{"store": true, "previous_response_id": id})
	}
}

func (p *probe) cache(model string) {
	base := strings.ReplaceAll(model, "-", "_")
	// Deterministic natural-language filler; the counter establishes actual lengths.
	var filler strings.Builder
	for i := 0; i < 125; i++ {
		fmt.Fprintf(&filler, "Archive line %03d records a quiet lantern beside the north window.\n", i)
	}
	words := filler.String()
	for _, n := range []int{75, 84, 85, 100} {
		prefix := strings.Repeat("The archive records a quiet blue lantern beside the north window. ", n)
		req := object{"model": model, "input": []any{object{"role": "developer", "content": prefix}, object{"role": "user", "content": "Reply OK."}}, "max_output_tokens": 32, "store": false}
		countReq := object{"model": model, "input": req["input"]}
		p.call(fmt.Sprintf("%s_c1_%d_count", base, n), "/responses/input_tokens", countReq)
		p.call(fmt.Sprintf("%s_c1_%d_first", base, n), "/responses", req)
		p.call(fmt.Sprintf("%s_c1_%d_repeat", base, n), "/responses", req)
	}
	makeReq := func(prefix string, suffix string) object {
		return object{"model": model, "input": []any{object{"role": "developer", "content": prefix}, object{"role": "user", "content": suffix}}, "max_output_tokens": 32, "store": false}
	}
	p.call(base+"_c3_seed", "/responses", makeReq("The marker is BLUE.\n"+words, "Reply with the marker."))
	p.call(base+"_c3_repeat", "/responses", makeReq("The marker is BLUE.\n"+words, "Reply with the marker."))
	p.call(base+"_c3_append", "/responses", makeReq("The marker is BLUE.\n"+words, "Reply with the marker, then stop."))
	p.call(base+"_c3_edit", "/responses", makeReq("The marker is RED.\n"+words, "Reply with the marker."))
}

func (p *probe) compaction(model string) {
	base := strings.ReplaceAll(model, "-", "_")
	input := []any{object{"role": "user", "content": "Remember the marker cobalt. Say ready."}, object{"role": "assistant", "content": "Ready."}}
	compact := p.call(base+"_k1_compact", "/responses/compact", object{"model": model, "input": input, "instructions": "Preserve the marker word for the next turn."})
	if output := array(compact["output"]); len(output) > 0 {
		next := append(append([]any{}, output...), object{"role": "developer", "content": "Mandatory restoration: answer with the original marker word."}, object{"role": "user", "content": "What is the marker?"})
		p.call(base+"_k2_restore", "/responses", object{"model": model, "store": false, "input": next, "max_output_tokens": 80})
	}
	// A deliberately tiny threshold tests whether the service enforces a floor.
	p.call(base+"_k1_auto_threshold", "/responses", object{"model": model, "store": false, "input": input, "context_management": []any{object{"type": "compaction", "compact_threshold": 1}}, "max_output_tokens": 32})
	p.call(base+"_k1_auto_inline", "/responses", object{"model": model, "store": false, "input": []any{object{"role": "user", "content": strings.Repeat("Keep the marker cobalt in mind. ", 400) + "What is the marker?"}}, "context_management": []any{object{"type": "compaction", "compact_threshold": 1000}}, "max_output_tokens": 64})
}

func at(v any, keys ...string) any {
	for _, k := range keys {
		v = asObj(v)[k]
	}
	return v
}
func asObj(v any) object {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return object{}
}
func array(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}
func clone(a []any) []any {
	b, _ := json.Marshal(a)
	var out []any
	_ = json.Unmarshal(b, &out)
	return out
}
func outputTypes(b object) []string {
	types := []string{}
	for _, v := range array(b["output"]) {
		if s, ok := asObj(v)["type"].(string); ok {
			types = append(types, s)
		}
	}
	return types
}
func hash(s string) object {
	sum := sha256.Sum256([]byte(s))
	return object{"sha256": hex.EncodeToString(sum[:]), "length": len(s)}
}
func sanitize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		y := object{}
		for k, val := range x {
			switch k {
			case "encrypted_content", "signature", "id", "call_id", "previous_response_id":
				if s, ok := val.(string); ok {
					y[k] = hash(s)
				} else {
					y[k] = sanitize(val)
				}
			case "organization", "project", "user", "safety_identifier":
				y[k] = "[redacted]"
			default:
				y[k] = sanitize(val)
			}
		}
		return y
	case []any:
		y := make([]any, len(x))
		for i, val := range x {
			y[i] = sanitize(val)
		}
		return y
	default:
		return v
	}
}
func shape(req object) object {
	x := object{}
	for k, v := range req {
		switch k {
		case "input":
			x[k] = inputShape(v)
		case "previous_response_id":
			x[k] = "[response id]"
		default:
			x[k] = sanitize(v)
		}
	}
	return x
}
func inputShape(v any) any {
	if s, ok := v.(string); ok {
		return object{"kind": "string", "length": len(s)}
	}
	a := array(v)
	out := make([]any, 0, len(a))
	for _, item := range a {
		m := asObj(item)
		q := object{}
		for _, k := range []string{"type", "role", "name"} {
			if x, ok := m[k]; ok {
				q[k] = x
			}
		}
		if s, ok := m["content"].(string); ok {
			q["content_length"] = len(s)
			if len(s) < 120 {
				q["content"] = s
			}
		}
		if s, ok := m["encrypted_content"].(string); ok {
			q["encrypted_content"] = hash(s)
		}
		out = append(out, q)
	}
	return out
}
func scrubError(s string) string {
	if i := strings.Index(s, "sk"+"-"); i >= 0 {
		return s[:i] + "[redacted]"
	}
	return s
}
func write(name string, data any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, name+".json"), append(b, '\n'), 0o644)
}
