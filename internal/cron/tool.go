package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

// Tool exposes the scheduler to the agent so a persona can set its own
// reminders or recurring messages without operator intervention.
func Tool(scheduler *Scheduler) tool.Definition {
	return tool.Definition{
		Name: "schedule_message",
		Description: "给自己定定时消息或提醒。action=add 添加任务（需要 text，加 after_seconds 表示多少秒后发一次，加 interval_seconds 表示每隔多少秒重复）；" +
			"action=remove 删除任务（需要 id）；action=list 查看本会话的定时任务。" +
			"进阶用法——报备跟进：当你要求对方回来后说一声（到家报平安/忙完叫我/醒了叫我），顺手排一条 30-60 分钟后的一次性跟进，" +
			"对方没回你就主动问一句，别让话题烂尾；对方提前回了也没关系，跟进消息写得宽松一点（比如「到家了没？都安顿好了当我没说～」）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"add", "remove", "list"},
				},
				"text": map[string]any{
					"type":        "string",
					"description": "到点要发送的消息内容",
				},
				"after_seconds": map[string]any{
					"type":        "integer",
					"description": "多少秒后发送一次（一次性任务）",
				},
				"interval_seconds": map[string]any{
					"type":        "integer",
					"description": "每隔多少秒重复发送（周期任务）",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "要删除的任务 id",
				},
			},
			"required": []string{"action"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    10 * time.Second,
		Handler: func(_ context.Context, call tool.Call) (tool.Result, error) {
			if scheduler == nil {
				return tool.Result{Error: "定时任务未启用"}, nil
			}
			var args struct {
				Action          string `json:"action"`
				Text            string `json:"text"`
				AfterSeconds    int    `json:"after_seconds"`
				IntervalSeconds int    `json:"interval_seconds"`
				ID              string `json:"id"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return tool.Result{Error: "参数解析失败"}, nil
			}
			switch strings.ToLower(strings.TrimSpace(args.Action)) {
			case "add":
				return addJob(scheduler, call, args.Text, args.AfterSeconds, args.IntervalSeconds), nil
			case "remove":
				return removeJob(scheduler, call, args.ID), nil
			case "list":
				return listJobs(scheduler, call), nil
			default:
				return tool.Result{Error: "action 只能是 add/remove/list"}, nil
			}
		},
	}
}

func addJob(
	scheduler *Scheduler,
	call tool.Call,
	text string,
	afterSeconds, intervalSeconds int,
) tool.Result {
	text = strings.TrimSpace(text)
	if text == "" {
		return tool.Result{Error: "text 不能为空"}
	}
	if call.Actor.ChatID == "" {
		return tool.Result{Error: "当前会话不可用"}
	}
	job := Job{
		ID:       fmt.Sprintf("ai-%d", time.Now().UnixNano()),
		Name:     "ai-scheduled",
		ChatType: call.Actor.ChatType,
		ChatID:   call.Actor.ChatID,
		SelfID:   call.Actor.SelfID,
		Text:     text,
	}
	switch {
	case intervalSeconds > 0:
		job.Interval = time.Duration(intervalSeconds) * time.Second
	case afterSeconds > 0:
		job.Once = true
		job.Interval = time.Duration(afterSeconds) * time.Second
		job.NextRun = time.Now().Add(job.Interval)
	default:
		return tool.Result{Error: "需要 after_seconds 或 interval_seconds"}
	}
	if err := scheduler.Add(job); err != nil {
		return tool.Result{Error: err.Error()}
	}
	if job.Once {
		return tool.Result{Content: fmt.Sprintf(
			"已定好：%d 秒后发「%s」（id=%s）", afterSeconds, text, job.ID)}
	}
	return tool.Result{Content: fmt.Sprintf(
		"已定好：每 %d 秒发「%s」（id=%s）", intervalSeconds, text, job.ID)}
}

func removeJob(scheduler *Scheduler, call tool.Call, id string) tool.Result {
	id = strings.TrimSpace(id)
	if id == "" {
		return tool.Result{Error: "id 不能为空"}
	}
	for _, job := range scheduler.List() {
		if job.ID == id && job.ChatID == call.Actor.ChatID {
			removed, err := scheduler.RemoveWithError(id)
			if err != nil {
				return tool.Result{Error: err.Error()}
			}
			if removed {
				return tool.Result{Content: "已删除定时任务 " + id}
			}
		}
	}
	return tool.Result{Error: "没找到这个任务"}
}

func listJobs(scheduler *Scheduler, call tool.Call) tool.Result {
	jobs := scheduler.List()
	lines := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.ChatID != call.Actor.ChatID {
			continue
		}
		kind := "周期"
		if job.Once {
			kind = "一次性"
		}
		lines = append(lines, fmt.Sprintf(
			"%s | %s | %s | 下次 %s | %s",
			job.ID, kind, job.Text,
			job.NextRun.Format("01-02 15:04"), job.Interval,
		))
	}
	if len(lines) == 0 {
		return tool.Result{Content: "本会话没有定时任务"}
	}
	return tool.Result{Content: strings.Join(lines, "\n")}
}
