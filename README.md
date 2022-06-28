# offliner

go后端启动http服务，该服务可以：
    解析短链接返回保存的网页
    提供配置页面（该服务交互全部由web承载）
    
    可以配置自定义域名，通过修改dns实现？
    
前后端分离，前端使用react制作静态页面

不依赖中心化的数据同步服务，要同步数据可以将数据目录设置到同步盘
    用sqlite持久化数据，用日志跟踪最近操作
        日志让多机可以并发操作不同的key
        但同一个key，要确保操作的先后顺序，要解决多机的时间同步问题（以用户最后一次操作为准，但还是可能有脏写）
        组合sqlite查询（持久数据）和内存中的数据结构（近期数据）
        
    要保存的数据有：
        文件数据：
            单文件网页 -- 只读文件，无同步问题
        kvdb：
            网页源地址
            单文件网页文件名

保存网页
    需要支持从浏览器插件保存，因为授权内容 offliner 自身无法访问
    可选保存为pdf，或者单文件html，提高可用性
    保存的网页文件需要禁用掉查看时访问网络
    给每个保存的网页添加顶层快捷按钮，可直接修改、删除等，也可跳转至配置页
    
    保存方案调研：
    https://github.com/gildas-lormeau/single-file-cli  需要docker