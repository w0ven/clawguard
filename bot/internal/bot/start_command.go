package bot

import (
	tele "gopkg.in/telebot.v3"
)

// handleStartCommand 回复 /start。
// 群内：极简欢迎；私聊：欢迎 + 引导到 /help。
func (s *Service) handleStartCommand(c tele.Context) error {
	if c == nil {
		return nil
	}
	chat := c.Chat()
	if chat == nil {
		return nil
	}
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
	}
	if chat.Type == tele.ChatPrivate {
		return c.Send(privateStartText(), opts)
	}
	return c.Send(groupStartText(), opts)
}

// handleHelpCommand 回复 /help。群内和私聊都输出完整命令说明。
func (s *Service) handleHelpCommand(c tele.Context) error {
	if c == nil {
		return nil
	}
	chat := c.Chat()
	if chat == nil {
		return nil
	}
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
	}
	return c.Send(helpText(), opts)
}

func groupStartText() string {
	return `<b>ClawGuard</b> — Telegram 群管理机器人

入群验证 · 关键词过滤 · AI 内容审核 · 信任毕业制

发送 /help 查看命令列表。`
}

func privateStartText() string {
	return `<b>ClawGuard</b> — Telegram 群管理机器人

功能概览：
• 入群验证（CAS 黑名单 / Bio 预检 / 人机验证）
• 规则过滤（关键词 / 正则 / 链接白名单 / 未毕业限制）
• AI 内容审核（未毕业用户 + 触发关键词兜底）
• 图片视觉审核（广告图 / 二维码识别）
• 跨聊天引用识别（展开 ExternalReply 内容送审）
• 未毕业用户发言前 Bio 审核（防入群后改简介加广告）
• 信任毕业制（积累干净发言自动毕业，降低误判）
• 警告系统 · 关键词回复 · 审计日志

发送 /help 查看命令列表。
Web 管理面板需管理员通过 /config 获取一次性登录链接。`
}

func helpText() string {
	return `<b>命令列表</b>

<b>群内（仅管理员）</b>
/status — 本群今日统计
/trust — 查用户信任分（回复消息或 @用户）
/warn — 警告用户 <code>/warn @user 原因</code>
/unban — 解封用户 <code>/unban @user</code>
/spam — 快捷封禁（回复消息 / @用户 / user_id）
/cas — 查询用户 CAS 黑名单状态
/warn_status — 查看警告记录
/config — 获取管理面板一次性登录链接

<b>私聊（仅管理员）</b>
/start — 机器人介绍
/help — 命令列表
/status — 服务运行状态
/trust — 查询用户信任分 <code>/trust &lt;user_id&gt;</code>
/config — 获取管理面板链接

<b>接入流程</b>
1. 将 bot 加入群并授予管理员权限（封禁用户 / 删除消息 / 邀请用户）
2. 由 owner 在管理面板「群授权」中登记群 chat_id
3. 未授权群会被自动退出
4. 默认策略立即生效，可按群覆盖`
}
