package command

// pluginCommands 返回 Plugin 类别的内置命令
func (r *Registry) pluginCommands() []Command {
	return []Command{
		{Name: "plugin list", Hidden: true, Description: "列出插件", Category: "Plugin",
			Handler: func(args string) (string, error) {
				if r.OnPlugins != nil {
					return r.OnPlugins(), nil
				}
				return "Plugin listing is " + ErrNotImplemented, nil
			}},
		{Name: "plugin install", Hidden: true, Description: "安装插件", Usage: "/plugin install <url|path>", Category: "Plugin",
			Handler: func(args string) (string, error) {
				if r.OnPluginInstall != nil {
					return r.OnPluginInstall(args), nil
				}
				return "Plugin install not initialized", nil
			}},
		{Name: "plugin enable", Hidden: true, Description: "启用插件", Usage: "/plugin enable <id>", Category: "Plugin",
			Handler: func(args string) (string, error) {
				if r.OnPluginEnable != nil {
					return r.OnPluginEnable(args), nil
				}
				return "Plugin enable not initialized", nil
			}},
		{Name: "plugin disable", Hidden: true, Description: "禁用插件", Usage: "/plugin disable <id>", Category: "Plugin",
			Handler: func(args string) (string, error) {
				if r.OnPluginDisable != nil {
					return r.OnPluginDisable(args), nil
				}
				return "Plugin disable not initialized", nil
			}},
		{Name: "reload-plugins", Hidden: true, Description: "重新加载所有插件", Category: "Plugin",
			Handler: func(args string) (string, error) {
				if r.OnReloadPlugins != nil {
					return r.OnReloadPlugins(), nil
				}
				return "Plugin reload not initialized", nil
			}},
	}
}
