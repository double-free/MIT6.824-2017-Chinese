### 简介
---
在 lab1 中我们将建立一个 MapReduce 库，学习如何使用 Go 建立一个容错的分布式系统。在 Part A 我们需要写一个简单的 MapReduce 程序。在 Part B我们需要实现一个 Master 为 MapReduce 的 Workers 安排任务，并处理 Workers 出现的错误。其原理可以参考[这篇论文](http://nil.csail.mit.edu/6.824/2017/papers/mapreduce.pdf)。

### 序言
---
mapreduce 包提供了一个简单的 Map/Reduce 库，应用程序通常调用 `master.go/Distributed()` 来开始一项任务（并行），同样也能调用 `master.go/Sequential()` 来串行执行，方便debug。

代码的执行流程为：
1. 应用提供一系列的输入文件，一个 map 函数，一个 reduce 函数，以及 reduce 任务的数量(`nReduce`)。
2.  Master 开启一个 RPC(Remote Procedure Call) 服务，然后等待 Workers 来注册（使用 `master.go/Register()`）。当有 Task 时，`schedule.go/schedule()` 决定如何将这些 Task 指派给 Workers，以及如何处理 Workers 的错误。
3. Master 将每个输入文件都当作 Task，然后调用 `common_map.go/doMap()`。这个过程既可以直接进行（串行模式），也可以通过 RPC 来让 Workers 做。 每次调用 `doMap()` 都会读取相应的文件，运行 map 函数，将 key/value 结果写入 `nReduce` 个中间文件之中。在 map 结束后总共会生成 `nMap * nReduce` 个文件。命名格式为：
**前缀-map编号-reduce编号**
例如，如果有 2 个 map tasks 以及 3 个 reduce tasks，map 回生成 2*3 = 6 个中间文件。
```
mrtmp.xxx-0-0
mrtmp.xxx-0-1
mrtmp.xxx-0-2
mrtmp.xxx-1-0
mrtmp.xxx-1-1
mrtmp.xxx-1-2
```
每个 Worker 必须能够读取其他 Worker 写入的文件（信息共享）。真正的分布式系统会使用分布式存储来实现不同机器之间的共享，在这里我们将所有的 Workers 运行在 **一台电脑上**，并使用本地文件系统。
4. 此后 Master 会调用 `common_reduce.go/doReduce()`，与 `doMap()` 一样，它也能直接完成或者通过工人完成。`doReduce()` 将按照 reduce task 编号来汇总，生成 `nReduce` 个结果文件。例如上面的例子中按照如下分组进行汇总：
```
  // reduce task 0
  mrtmp.xxx-0-0
  mrtmp.xxx-1-0
  // reduce task 1
  mrtmp.xxx-0-1
  mrtmp.xxx-1-1
  // reduce task 2
  mrtmp.xxx-0-2
  mrtmp.xxx-1-2
```
5. 此后 Master 调用 `master_splitmerge.go/mr.merge()` 将所有生成的文件整合为一个输出。
6. Master 发送 `Shutdown RPC` 给所有 Workers，然后关闭自己的 RPC 服务。

### Part I: Map/Reduce input and output
---

在我们实现 Map/Reduce 之前，首先要修复一个串行实现。给出的源码缺少两个关键部分：
1. 划分 Map 输出的函数 `doMap()`。
2. 汇总 Reduce 输入的函数 `doReduce()`。

首先建议阅读 `common.go`，这里定义了会用到的数据类型，文件命名方法等。

##### doMap() 函数
首先总结一下 Map 的过程，它对于每个 Map Task，都会进行以下操作：
1. 从某个数据文件 A.txt 中读取数据。
2. 自定义函数 mapF 对 A.txt 中的文件进行解读，形成一组 {Key, Val} 对。
3. 生成 nReduce 个子文件 A_1, A_2, ..., A_nReduce。
4. 利用 {Key, Val} 中的 Key 值做哈希，将得到的值对 nReduce 取模，以此为依据将其分配到子文件之中。

```go
func doMap(
	jobName string, // the name of the MapReduce job
	mapTaskNumber int, // which map task this is
	inFile string,
	nReduce int, // the number of reduce task that will be run ("R" in the paper)
	mapF func(file string, contents string) []KeyValue,
) {
	// 查看参数
	fmt.Printf("Map: job name = %s, input file = %s, map task id = %d, nReduce = %d\n",
		 jobName, inFile, mapTaskNumber, nReduce);

	// 读取输入文件
	bytes, err := ioutil.ReadFile(inFile)
	if (err != nil) {
		// log.Fatal() 打印输出并调用 exit(1)
		log.Fatal("Unable to read file: ", inFile)
	}

	// 解析输入文件为 {key,val} 数组
	kv_pairs := mapF(inFile, string(bytes))

	// 生成一组 encoder 用来将 {key,val} 保存至对应文件
	encoders := make([]*json.Encoder, nReduce);
	for reduceTaskNumber := 0; reduceTaskNumber < nReduce; reduceTaskNumber++ {
		filename := reduceName(jobName, mapTaskNumber, reduceTaskNumber)
		file_ptr, err := os.Create(filename)
		if (err != nil) {
			log.Fatal("Unable to create file: ", filename)
		}
		// defer 后不能用括号
		defer file_ptr.Close()
		encoders[reduceTaskNumber] = json.NewEncoder(file_ptr);
	}

	// 利用 encoder 将 {key,val} 写入对应的文件
	for _, key_val := range kv_pairs {
		key := key_val.Key
		reduce_idx := ihash(key) % nReduce
		err := encoders[reduce_idx].Encode(key_val)
		if (err != nil) {
			log.Fatal("Unable to write to file")
		}
	}
}
```

##### doReduce() 函数

```Go
func doReduce(
	jobName string, // the name of the whole MapReduce job
	reduceTaskNumber int, // which reduce task this is
	outFile string, // write the output here
	nMap int, // the number of map tasks that were run ("M" in the paper)
	reduceF func(key string, values []string) string,
) {
	// 查看参数
	fmt.Printf("Reduce: job name = %s, output file = %s, reduce task id = %d, nMap = %d\n",
		 jobName, outFile, reduceTaskNumber, nMap);
	
	// 建立哈希表，以 slice 形式存储同一 key 的所有 value
	kv_map := make(map[string]([]string))
	
	// 读取同一个 reduce task 下的所有文件，保存至哈希表
	for mapTaskNumber := 0; mapTaskNumber < nMap; mapTaskNumber++ {
		filename := reduceName(jobName, mapTaskNumber, reduceTaskNumber)
		f, err := os.Open(filename)
		if (err != nil) {
			log.Fatal("Unable to read from: ", filename)
		}
		defer f.Close()

		decoder := json.NewDecoder(f)
		var kv KeyValue
		for ; decoder.More(); {
			err := decoder.Decode(&kv)
			if (err != nil) {
				log.Fatal("Json decode failed, ", err)
			}
			kv_map[kv.Key] = append(kv_map[kv.Key], kv.Value)
		}
	}

	// 对哈希表所有 key 进行升序排序
	keys := make([]string, 0,len(kv_map))
	for k,_ := range kv_map {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 利用自定义的 reduceF 函数处理同一 key 下的所有 val
	// 按照 key 的顺序将结果以 {key, new_val} 形式输出
	outf, err := os.Create(outFile)
	if (err != nil) {
		log.Fatal("Unable to create file: ", outFile)
	}
	defer outf.Close()
	encoder := json.NewEncoder(outf)
	for _,k := range keys {
		encoder.Encode(KeyValue{k, reduceF(k, kv_map[k])})
	}
}
```

最后 `go test -run Sequential` 通过。

### Part II: Single-worker word count
---
现在我们将实现一个简单的 Map/Reduce 案例：词频统计。在 `main/wc.go` 中有需要实现的 `mapF()` 以及 `reduceF()` 方法。我们需要完成统计输入文件中每个单词出现频率的功能。
我们需要将文件名和文件的内容作为参数传递给 `mapF()`，它会把内容分成一个个单词，并返回 {Key, Value} 的 slice。其中，Key 就是单词。`reduceF()` 会处理一个 Key 下的所有 Value，然后返回单词出现总数。

```Go
func mapF(filename string, contents string) []mapreduce.KeyValue {
	// TODO: you have to write this function
	// words := strings.Fields(contents)
	f := func(c rune) bool {
		return !unicode.IsLetter(c)
	}
	words := strings.FieldsFunc(contents, f)
	kv_slice := make([]mapreduce.KeyValue, 0, len(words))
	for _,word := range words {
		kv_slice = append(kv_slice, mapreduce.KeyValue{word, "1"})
	}
	return kv_slice
}
```

```Go
func reduceF(key string, values []string) string {
	// TODO: you also have to write this function
	var sum int
	for _,str := range values {
		i, err := strconv.Atoi(str)
		if (err != nil) {
			log.Fatal("Unable to convert ", str, " to int")
		}
		sum += i
	}
	return strconv.Itoa(sum)
}
```

实现较为简单，但是我犯了一个低级错误，即用`strings.Fields()` 来分割字符串。实际上，假设一个句子是：

"Where is he? Anyone knows?"

如果只用空格分开，本来是 "he" 就变成了 "he?"，导致统计出错。

### Part III: Distributing MapReduce tasks
---
目前为止我们都是串行地执行任务，Map/Reduce 最大的优势就是能够自动地并行执行普通的代码，不用开发者进行额外工作。在 Part III 我们会把任务分配给一组 worker thread，在多核上并行进行。虽然我们不在多机上进行，但是会用 RPC 来模拟分布式计算。

我们需要实现 `mapreduce/schedule.go` 的 `schedule()`，在一次任务中，Master 会调用两次 `schedule()`，一次用于 Map，一次用于 Reduce。`schedule()`将会把任务分配给 Workers，通常任务会比 Workers 数量多，因此 `schedule()` 会给每个 worker 一个 Task 序列，然后等待所有 Task 完成再返回。

`schedule()` 通过 `registerChan` 参数获取 Workers 信息，它会生成一个包含 Worker 的 RPC 地址的 string，有些 Worker 在调用 `schedule()` 之前就存在了，有的在调用的时候产生，他们都会出现在 `registerChan` 中。

`schedule()` 通过发送 `Worker.DoTask` RPC 调度 Worker 执行任务，可以用 `mapreduce/common_rpc.go` 中的 `call()` 函数发送。`call()` 的第一个参数是 Worker 的地址，可以从 `registerChan` 获取，第二个参数是 `"Worker.DoTask"` 字符串，第三个参数是 `DoTaskArgs` 结构体的指针，最后一个参数为 `nil`。

这是一个比较有含金量的练习。
`schedule()` 函数的调用发生在 `master.go` 中。首先，在`Distributed()` 中对 `schedule()` 做了一个再封装，使得其只需要一个参数判断是 Map 还是 Reduce。
```Go
func Distributed(jobName string, files []string, nreduce int, master string) (mr *Master) {
	mr = newMaster(master)
	mr.startRPCServer()
	go mr.run(jobName, files, nreduce,
		func(phase jobPhase) {
			ch := make(chan string)
			go mr.forwardRegistrations(ch)
			schedule(mr.jobName, mr.files, mr.nReduce, phase, ch)
		},
		func() {
			mr.stats = mr.killWorkers()
			mr.stopRPCServer()
		})
	return
}
```
这里就是实际使用 `schedule()` 的地方。一次用于 Map，一次用于 Reduce。
```Go
func (mr *Master) run(jobName string, files []string, nreduce int,
	schedule func(phase jobPhase),
	finish func(),
) {
	mr.jobName = jobName
	mr.files = files
	mr.nReduce = nreduce

	fmt.Printf("%s: Starting Map/Reduce task %s\n", mr.address, mr.jobName)

	schedule(mapPhase)
	schedule(reducePhase)
	finish()
	mr.merge()

	fmt.Printf("%s: Map/Reduce task completed\n", mr.address)

	mr.doneChannel <- true
}
```
了解完调用后，还需要重点了解的是[ RPC 机制](https://golang.org/pkg/net/rpc/)。搞懂 `call()` 的参数形式。
先上一个错误的写法，该写法导致文件生成不全，程序不能顺利结束：
```Go
func schedule(jobName string, mapFiles []string, nReduce int, phase jobPhase, registerChan chan string) {
	var ntasks int
	var n_other int // number of inputs (for reduce) or outputs (for map)
	switch phase {
	case mapPhase:
		ntasks = len(mapFiles)
		n_other = nReduce
	case reducePhase:
		ntasks = nReduce
		n_other = len(mapFiles)
	}

	fmt.Printf("Schedule: %v %v tasks (%d I/Os)\n", ntasks, phase, n_other)

	// Part III code
	// 错误，多个 gotoutine 共用一个 DoTaskArgs，在多线程中有问题
	var taskArgs DoTaskArgs
	taskArgs.JobName = jobName
	taskArgs.Phase = phase
	taskArgs.NumOtherPhase = n_other
	var wait_group sync.WaitGroup;
	for i := 0; i < ntasks; i++ {
		wait_group.Add(1)
		// 错误，i++ 在主线程执行，goroutine 里的 i 值受到影响
		go func() {
			fmt.Printf("Now: %dth task\n", i)
			defer wait_group.Done()
			taskArgs.TaskNumber = i
			if (phase == mapPhase) {
				taskArgs.File = mapFiles[i]
			}
			worker := <-registerChan
			if (call(worker, "Worker.DoTask", &taskArgs, nil) != true) {
				log.Fatal("RPC call error, exit")
			}
			// 错误，导致死锁
			registerChan <- worker
		}()
	}
	wait_group.Wait()
	fmt.Printf("Schedule: %v phase done\n", phase)
}
```
显然，这是对多线程编程理解不够深入导致的。脑子里需要时刻有根弦，凡是要修改的数据，都不要共享，否则就记得加锁。另外，channel 操作时，需要小心死锁。

以下是正确的写法。
```Go
func schedule(jobName string, mapFiles []string, nReduce int, phase jobPhase, registerChan chan string) {
	var ntasks int
	var n_other int // number of inputs (for reduce) or outputs (for map)
	switch phase {
	case mapPhase:
		ntasks = len(mapFiles)
		n_other = nReduce
	case reducePhase:
		ntasks = nReduce
		n_other = len(mapFiles)
	}

	fmt.Printf("Schedule: %v %v tasks (%d I/Os)\n", ntasks, phase, n_other)

	// All ntasks tasks have to be scheduled on workers, and only once all of
	// them have been completed successfully should the function return.
	// Remember that workers may fail, and that any given worker may finish
	// multiple tasks.
	//
	// TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO
	//

	// Part III code
	var wait_group sync.WaitGroup;

	for i := 0; i < ntasks; i++ {
		wait_group.Add(1)
		// 每个 task 独自拥有一个 DoTaskArgs
		var taskArgs DoTaskArgs
		taskArgs.JobName = jobName
		taskArgs.Phase = phase
		taskArgs.NumOtherPhase = n_other
		taskArgs.TaskNumber = i
		if (phase == mapPhase) {
			taskArgs.File = mapFiles[i]
		}
		go func() {
			// fmt.Printf("Now: %dth task\n", task_id)
			defer wait_group.Done()
			worker := <-registerChan
			if (call(worker, "Worker.DoTask", &taskArgs, nil) != true) {
				log.Fatal("RPC call error, exit")
			}
			// 非常关键，完成后再将 worker 放回
			go func() {registerChan <- worker}()
		}()
	}
	wait_group.Wait()
	fmt.Printf("Schedule: %v phase done\n", phase)
}
```
最后 `go test -run TestBasic` 通过。

### Part IV: Handling worker failures
---
在该部分中我们需要让 Master 能够处理 failed Worker。MapReduce 使这个处理相对简单，因为如果一个 Worker fails，Master 交给它的任何任务都会失败。此时 Master 需要把任务交给另一个 Worker。

一个 RPC 出错并不一定表示 Worker 没有执行任务，有可能只是 reply 丢失了，或是 Master 的 RPC 超时了。因此，有可能两个 Worker 都完成了同一个任务。同样的任务会生成同样的结果，所以这样并不会引发什么问题。并且，在该 lab 中，每个 Task 都是序列执行的，这就保证了结果的整体性。

实现较简单，将 Part III 中的 goroutine 做小幅度修改即可。加入无线循环使得在 call 返回 false 的时候另选一个 worker 重试，返回 true 的时候将 worker 放回 ch，跳出循环。
```Go
...
		go func() {
			// fmt.Printf("Now: %dth task\n", task_id)
			defer wait_group.Done()
			// 加入无限循环，只要任务没完成，就换个 worker 执行
			for {
				worker := <-registerChan
				if (call(worker, "Worker.DoTask", &taskArgs, nil) == true) {
					// 非常关键，完成后再将 worker 放回
					go func() {registerChan <- worker}()
					break
				}
			}
		}()
...
```

修改后 `go test -run Failure` 成功。

### Part V: Inverted index generation (optional)
---
该部分的要求是，统计出所有包含某个词的文件。并以下列形式输出：
```
<单词>: <文件个数> <排序后的文件名列表>
```
相比 Part II 中的词频统计，思想是类似的。
首先在 mapF 里对文件的内容分词，维护一个哈希表，Key 是单词，Value 是文件名。这样可以去掉重复的词。最后再把哈希表转为输出要求的格式。
```Go
func mapF(document string, value string) (res []mapreduce.KeyValue) {
	// TODO: you should complete this to do the inverted index challenge
	f := func(c rune) bool {
		return !unicode.IsLetter(c)
	}
	words := strings.FieldsFunc(value, f)
	// 区别1. 采取 HashMap 去掉重复单词
	map_doc := make(map[string]string, 0)
	for _,word := range words {
		map_doc[word] = document
	}
	res = make([]mapreduce.KeyValue, 0, len(map_doc))
	for k,v := range map_doc {
		res = append(res, mapreduce.KeyValue{k, v})
	}
	return
}
```
mapF 返回的数组会经过哈希处理分配到 nReduce 个文件中。doReduce 则会将隶属于同样的 reduce_task_id 的文件的 {key, val} 整合为 {key, []val}。在本例中就相当于 {单词，[]文件名}。
在 reduceF 里主要是对每个 key 下的所有 value 进行处理。注意，它并不负责输出，只负责返回一个 newVal，最后由 doReduce 统一以 {key, newVal} 形式输出。因此我们只需要把 []文件名 进行排序后，按要求格式转为一个 string 即可。
```Go
func reduceF(key string, values []string) string {
	// TODO: you should complete this to do the inverted index challenge
	nDoc := len(values)
	sort.Strings(values)
	var buf bytes.Buffer;
	buf.WriteString(strconv.Itoa(nDoc))
	buf.WriteRune(' ')
	for i,doc := range values {
		buf.WriteString(doc)
		if (i != nDoc-1) {
			buf.WriteRune(',')
		}
	}
	return buf.String()
}
```
最后 `./test-ii.sh` 测试通过。
