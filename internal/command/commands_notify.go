package command

import "github.com/anchorandfish/whale-code/internal/util"

// notifyCommands 返回 Notify 类别的内置命令（N24 通知系统）
func (r *Registry) notifyCommands() []Command {
	return []Command{
		{Name: "notify list", Hidden: true, Description: "查看通知队列和历史", Category: "Notify",
			Handler: func(args string) (string, error) {
				if r.OnNotifyList != nil {
					return r.OnNotifyList(), nil
				}
				return util.Bi("Notification system is not initialized", "通知系统未初始化"), nil
			}},
		{Name: "notify clear", Hidden: true, Description: "清空通知历史", Category: "Notify",
			Handler: func(args string) (string, error) {
				if r.OnNotifyClear != nil {
					return r.OnNotifyClear(), nil
				}
				return util.Bi("Notification system is not initialized", "通知系统未初始化"), nil
			}},
		{Name: "notify dismiss", Hidden: true, Description: "关闭当前通知", Category: "Notify",
			Handler: func(args string) (string, error) {
				if r.OnNotifyDismiss != nil {
					return r.OnNotifyDismiss(), nil
				}
				return util.Bi("Notification system is not initialized", "通知系统未初始化"), nil
			}},
	}
}
