Part 2B
---
在这个部分我们希望 Raft 能保持一个一致的操作 log。利用 `Start()` 给 leader 的 log 增加一个新的操作。Leader 会将这个新的操作通过 `AppendEntries` RPC 给其他的 server。

>**Task**
Implement the leader and follower code to append new log entries. This will involve implementing `Start()`, completing the `AppendEntries` RPC structs, sending them, fleshing out the `AppendEntry` RPC handler, and advancing the `commitIndex` at the leader. Your first goal should be to pass the `TestBasicAgree()` test (in `test_test.go`). Once you have that working, you should get all the 2B tests to pass (`go test -run 2B`).

论文重点翻译
---
### 5.3 Log replication
一旦选举出了一个 leader，它就会开始服务 client 的请求。每个请求包含了一个需要被 replicated state machines 执行的命令。leader 将命令作为一个新 entry 添加到自己的 log 中，然后并行地发送 `AppendEntries` RPC 给其他的 server。直到所有的 follower 返回 append 成功，再执行命令。否则一直重试发送 `AppendEntries`。
一个典型的 LogEntry 可定义如下：
```go
type LogEntry struct {
index   int  // start from 1
term    int
command interface{} // 数据类型与 Start() 函数的参数一致
}
```
```
type AppendEntriesArgs struct {
// Part A
Term     int
LeaderId int

// Part B
PrevLogIndex int
PrevLogTerm  int
Entries      []LogEntry
LeaderCommit int
}
```

理解与实现
---
首先需要深刻理解的是运作流程。其中涉及到几个状态的理解。

- **committed**

其含义是，该分布式系统 log 中已经达成一致的部分。是可以进行下一步 apply 的部分。

- **applied**

已经committed，并发送到 applyCh 信道的 log 部分。

client 通过 `Start()` 指派指令，Leader 收到指令后，只是将其追加到 log 中，暂时不发送，直到下一次 `broadcastAppendEntries()` 时再发送。也就是说，是凭借心跳包发出的。如果这个时段没有追加新指令，则就是单纯的心跳包，否则就是带了数据的。
Follower 接收到 AppendEntries 这个RPC，会首先找到同步的 log，然后将新指令追加到之后。参数还包括 Leader 此时的 commitIndex，会用来更新 Follower 的 commitIndex。这是为了让所有 Follower 与 Leader 进度保持一致。注意，只有 Leader 的 commitIndex 是根据 matchIndex 进行主动更新的，所有 Follower 都是通过 AppendEntries 中的 LeaderCommit 来被动更新。
当达成一致以后，就能 apply 了。apply 的方法其实就是将命令打包成 ApplyMsg，并发给每个 server 自己的 applyCh。

理清了这些关系，写出一个能通过 BasicAgree 测试的程序就不难了。
此后就是不断地 debug。我遇到的问题记录在后半部分，希望有所启发。

还需要阅读 `raft/config.go` 中的 `one()` 函数，该函数会在整个测试用例中频繁使用。
```go
func (cfg *config) one(cmd int, expectedServers int) int {
// 参数1: 需要达成 agreement 的命令
// 参数2: 期望有几个 server 达成一致
t0 := time.Now()
starts := 0
// 在 10s 内不断重复以下过程。注意每次 sleep 一段时间。如果一直找不到 Leader，
for time.Since(t0).Seconds() < 10 {
// try all the servers, maybe one is the leader.
// 遍历所有 server，找到 Leader 并开始一个新的 agreement (cmd)
index := -1  // 记录 cmd 存放的位置
for si := 0; si < cfg.n; si++ {
starts = (starts + 1) % cfg.n
var rf *Raft
cfg.mu.Lock()
if cfg.connected[starts] {
rf = cfg.rafts[starts]
}
cfg.mu.Unlock()
if rf != nil {
index1, _, ok := rf.Start(cmd)
if ok {
index = index1
break
}
}
}

if index != -1 {
// somebody claimed to be the leader and to have
// submitted our command; wait a while for agreement.
t1 := time.Now()
for time.Since(t1).Seconds() < 2 {
nd, cmd1 := cfg.nCommitted(index)
if nd > 0 && nd >= expectedServers {
// committed
if cmd2, ok := cmd1.(int); ok && cmd2 == cmd {
// and it was the command we submitted.
return index
}
}
time.Sleep(20 * time.Millisecond)
}
} else {
time.Sleep(50 * time.Millisecond)
}
}
cfg.t.Fatalf("one(%v) failed to reach agreement", cmd)
return -1
}
```


问题记录
---
1. **RPC 参数传递出现 nil 指针**

```
args at 0x0, reply at 0xc420011f50
panic: runtime error: invalid memory address or nil pointer dereference
```
发现是由于 LogEntry 中的 field 没有大写导致的。传递的 args 中包含了一个 LogEntry 的 slice。因此 LogEntry 中的所有 field 也应该大写。
```
type LogEntry struct {
Index   int // start from 1
Term    int
Command interface{} // 数据类型与 Start() 函数的参数一致
}
```

2. **没有正确处理 AppendEntries 返回 false 时的情况**

```go
/*
这样判断有一个很大的问题。由于 Leader 是并发的，会收到好几个 follower 的答复
第一个收到的 follower 的答复导致了 rf.currentTerm 更新
那么此后的答复都会是 reply.Term == rf.currentTerm
因此不能简单据此判断是 log entry 不合
*/
if reply.Term > rf.currentTerm {
// fail because of outdate
rf.mu.Lock()
rf.currentTerm = reply.Term
rf.updateStateTo(FOLLOWER)
rf.mu.Unlock()
} else {
// fail because of log inconsistency
// decrement nextIndex and retry
fmt.Printf("Leader %d: duplicate to follower %d failed, retry...\n", rf.me, server)
rf.nextIndex[server] -= 1
goto LOOP
}
```
3. **applyCh 阻塞**

代码重构了一次，忘了初始化 rf.applyCh。第二次遇到这类问题了。
4. **能通过 BasicAgree, 不能通过 FailAgree**

查了很久，发现原因在更新 commitIndex 上。Leader 自身还有一票，所以 count 应该初始化为 1。
```
// Part B: update rf.commitIndex
for i := rf.lastLogIndex(); i > rf.commitIndex; i-- {
count := 1	// 初始化为 1 因为自身也有一票
for server, v := range rf.matchIndex {
if server == rf.me {
continue
}
if v >= i {
count++
}
}
if count > len(rf.peers)/2 {
// majority
rf.commitIndex = i
fmt.Printf("Leader %d update commitIndex to %d, lastApplied = %d\n", rf.me, rf.commitIndex, rf.lastApplied)
break
}
}
```
这里有人可能会问，为什么不在更新 follower 的 nextIndex 和 matchIndex 时顺便更新 Leader 的，避免这里的特殊处理？
原因在于，每个 follower 的进度都不一样，到底按照谁的来更新呢？取最大当然可行，但是太麻烦了。本质上，Leader 的数据总是 up-to-date 的，所以这样处理比较好。

5. **通过了 FailAgree 的 105，但是 106 开始 Fail**

出现的状况是无法选举出 Leader。于是在 vote 过程排查。发现又是一个低级错误，在 `startElection()` 函数中忘了初始化两个新的field。细节，细节，细节。
```
args := RequestVoteArgs{}
args.Term = rf.currentTerm
args.CandidateId = rf.me
// Part B
args.LastLogIndex = rf.lastLogIndex()
args.LastLogTerm = rf.log[rf.lastLogIndex()].Term
```

6. **无法通过 TestRejoin2B 的 103**

该测试场景是当 Leader 断开又重连时，系统能否正常运作。
分析 log 发现旧的 Leader (假设为 leader0) 重连后改变了某个 Follower 的 log，这本来是不该发生的。查错发现，leader0 重连后，由于自身状态还是 Leader，会发送心跳包。第一个心跳包返回后，会修改 leader0 的 Term 为现在的 term，并将其转为 follower。然而，由于并发，另一个心跳包返回时，该 leader 的currentTerm 已经是最新了，而由于这时没有判断 state == LEADER，所以直接进入了 log 复制环节。
```
func (rf *Raft) broadcastAppendEntries() {
rf.mu.Lock()
defer rf.mu.Unlock()
for i, _ := range rf.peers {
if i == rf.me {
// skip self
continue
}
go func(server int) {
args := AppendEntriesArgs{}
reply := AppendEntriesReply{}
LOOP:
args.Term = rf.currentTerm
args.LeaderId = rf.me
args.PrevLogIndex = rf.nextIndex[server] - 1
args.PrevLogTerm = rf.log[args.PrevLogIndex].Term
args.LeaderCommit = rf.commitIndex
if rf.lastLogIndex() >= rf.nextIndex[server] {
args.Entries = rf.log[rf.nextIndex[server]:]
}
if rf.sendAppendEntries(server, &args, &reply) {
if reply.Success == true {
rf.nextIndex[server] += len(args.Entries)
rf.matchIndex[server] = rf.nextIndex[server] - 1
} else {
if reply.Term > rf.currentTerm {
// fail because of outdate
rf.mu.Lock()
rf.currentTerm = reply.Term
rf.updateStateTo(FOLLOWER)
rf.mu.Unlock()
}
// 非常关键的判断，不加会导致过时的 Leader 仍然能进行 log replication
if rf.state != LEADER {
return
}
if reply.LastMatchIndex != rf.matchIndex[server] {
// fail because of log inconsistency
// decrement nextIndex and retry
fmt.Printf("Leader %d: duplicate to follower %d failed, retry...\n", rf.me, server)
rf.nextIndex[server] = reply.LastMatchIndex + 1
goto LOOP
}
}
} else {
fmt.Printf("Network Error, Leader %d cannot reach server %d\n", rf.me, server)
}
}(i)
}
}
```

7. **有时无法通过 TestBackup2B**

偶尔才出现，不好排查。暂时作为遗留问题。
如下：
```
yuanyedeMacBook-Pro:raft Crow$ go test -run Backup2B
Test (2B): leader backs up quickly over incorrect follower logs ...
... Passed
PASS
ok  	raft	25.807s
yuanyedeMacBook-Pro:raft Crow$ go test -run Backup2B
Test (2B): leader backs up quickly over incorrect follower logs ...
2017/06/06 16:35:55 apply error: commit index=52 server=4 8236000563283034457 != server=2 3468179317868568601
exit status 1
FAIL	raft	25.682s
```
