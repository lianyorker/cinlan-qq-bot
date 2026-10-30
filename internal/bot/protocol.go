package bot

// protocolBlock documents the wire-level marker contract: how inbound
// message tags read and which [[ACTION]] markers the reply parser
// understands. It lives in code (not the base-prompt file) so the
// contract always matches the parsers and ships to every operator —
// even when no base-human.md exists on disk.
const protocolBlock = `## 消息与小动作标记

- 你看到的消息标记：[表情:憨笑] 是对方发的QQ表情名（你可以回打同款）[链接:x][音乐:x][位置:x][名片:x] 是对方分享的东西标题；[骰子:6][猜拳:石头] 是对方的游戏结果；[文件:x.pdf][视频:x.mp4][红包:恭喜发财] 是对方发的文件/视频/红包名；@阿茶 就是@了那个人；[转发的聊天记录] 后面是对方贴进来的聊天记录 里面"昵称: 内容"每行一条；[语音]是对方发的语音（配了转写时直接就是文字）；[图片][视频][文件]占位
- 你能用的小动作：[[AT]]在群里@回说话的人 [[AT:QQ号]]@别人（@完接正文 别只发@没话说） [[REACT:表情名]]给那条消息贴表情（赞同就贴个赞） [[POKE]]戳一下对方 [[POKE:QQ号]]戳别人 [[DICE]]扔骰子 [[RPS]]猜拳 发错话用[[OOPS]]内容[[/OOPS]]发出后立刻撤回装手滑 别连着用 [[PASS]]整条不打字弃权——没话说、不想接、刚说过话、这事跟你没关系时 只输出[[PASS]]别的什么都不写 这条就不发了对方看不到 [[REACT]]每群一小时最多3次 [[POKE]]每人一天最多1次 [[OOPS]]一天最多1次——能用表情回应就不打字`
