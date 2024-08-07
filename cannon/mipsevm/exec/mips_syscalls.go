package exec

import (
	"encoding/binary"
	"io"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/ethereum-optimism/optimism/cannon/mipsevm"
	"github.com/ethereum-optimism/optimism/cannon/mipsevm/memory"
)

// Syscall codes
const (
	SysMmap       = 5009
	SysMunmap     = 5011
	SysBrk        = 5012
	SysClone      = 5055
	SysExitGroup  = 5205
	SysRead       = 5000
	SysWrite      = 5001
	SysFcntl      = 5070
	SysExit       = 5058
	SysSchedYield = 5023
	SysGetTID     = 5178
	SysFutex      = 5194
	SysOpen       = 5002
	SysNanosleep  = 5034
)

// Noop Syscall codes
const (
	SysGetAffinity   = 5196
	SysMadvise       = 5027
	SysRtSigprocmask = 5014
	SysSigaltstack   = 5129
	SysRtSigaction   = 5013
	SysPrlimit64     = 5297
	SysClose         = 5003
	SysPread64       = 5016
	SysFstat64       = 5005
	SysOpenAt        = 5247
	SysReadlink      = 5087
	SysReadlinkAt    = 5257
	SysIoctl         = 5015
	SysEpollCreate1  = 5285
	SysPipe2         = 5287
	SysEpollCtl      = 5208
	SysEpollPwait    = 5272
	SysGetRandom     = 5313
	SysUname         = 5061
	SysStat64        = 5004
	SysGetuid        = 5100
	SysGetgid        = 5102
	SysLlseek        = 5008
	SysMinCore       = 5026
	SysTgkill        = 5225
)

// Profiling-related syscalls
// Should be able to ignore if we patch out prometheus calls and disable memprofiling
// TODO(cp-903) - Update patching for mt-cannon so that these can be ignored
const (
	SysSetITimer    = 5036
	SysTimerCreate  = 5216
	SysTimerSetTime = 5217
	SysTimerDelete  = 5220
	SysClockGetTime = 5222
)

// File descriptors
const (
	FdStdin         = 0
	FdStdout        = 1
	FdStderr        = 2
	FdHintRead      = 3
	FdHintWrite     = 4
	FdPreimageRead  = 5
	FdPreimageWrite = 6
)

// Errors
const (
	SysErrorSignal = ^uint64(0)
	MipsEBADF      = 0x9
	MipsEINVAL     = 0x16
	MipsEAGAIN     = 0xb
	MipsETIMEDOUT  = 0x91
)

// SysFutex-related constants
const (
	FutexWaitPrivate  = 128
	FutexWakePrivate  = 129
	FutexTimeoutSteps = 10_000
	FutexNoTimeout    = ^uint64(0)
	FutexEmptyAddr    = ^uint64(0)
)

// SysClone flags
// Handling is meant to support go runtime use cases
// Pulled from: https://github.com/golang/go/blob/go1.21.3/src/runtime/os_linux.go#L124-L158
const (
	CloneVm            = 0x100
	CloneFs            = 0x200
	CloneFiles         = 0x400
	CloneSighand       = 0x800
	ClonePtrace        = 0x2000
	CloneVfork         = 0x4000
	CloneParent        = 0x8000
	CloneThread        = 0x10000
	CloneNewns         = 0x20000
	CloneSysvsem       = 0x40000
	CloneSettls        = 0x80000
	CloneParentSettid  = 0x100000
	CloneChildCleartid = 0x200000
	CloneUntraced      = 0x800000
	CloneChildSettid   = 0x1000000
	CloneStopped       = 0x2000000
	CloneNewuts        = 0x4000000
	CloneNewipc        = 0x8000000

	ValidCloneFlags = CloneVm |
		CloneFs |
		CloneFiles |
		CloneSighand |
		CloneSysvsem |
		CloneThread
)

// Other constants
const (
	SchedQuantum = 100_000
	BrkStart     = 0x40000000
)

func GetSyscallArgs(registers *[32]uint64) (syscallNum, a0, a1, a2, a3 uint64) {
	syscallNum = registers[2] // v0

	a0 = registers[4]
	a1 = registers[5]
	a2 = registers[6]
	a3 = registers[7]

	return syscallNum, a0, a1, a2, a3
}

func HandleSysMmap(a0, a1, heap uint64) (v0, v1, newHeap uint64) {
	v1 = uint64(0)
	newHeap = heap

	sz := a1
	if sz&memory.PageAddrMask != 0 { // adjust size to align with page size
		sz += memory.PageSize - (sz & memory.PageAddrMask)
	}
	if a0 == 0 {
		v0 = heap
		//fmt.Printf("mmap heap 0x%x size 0x%x\n", v0, sz)
		newHeap += sz
	} else {
		v0 = a0
		//fmt.Printf("mmap hint 0x%x size 0x%x\n", v0, sz)
	}

	return v0, v1, newHeap
}

func HandleSysRead(a0, a1, a2 uint64, preimageKey [32]byte, preimageOffset uint64, preimageReader PreimageReader, memory *memory.Memory, memTracker MemTracker) (v0, v1, newPreimageOffset uint64) {
	// args: a0 = fd, a1 = addr, a2 = count
	// returns: v0 = read, v1 = err code
	v0 = uint64(0)
	v1 = uint64(0)
	newPreimageOffset = preimageOffset

	switch a0 {
	case FdStdin:
		// leave v0 and v1 zero: read nothing, no error
	case FdPreimageRead: // pre-image oracle
		effAddr := a1 & 0xFFFFFFFFFFFFFFF8
		memTracker.TrackMemAccess(effAddr)
		mem := memory.GetDoubleWord(effAddr)
		dat, datLen := preimageReader.ReadPreimage(preimageKey, preimageOffset)
		//fmt.Printf("reading pre-image data: addr: %08x, offset: %d, datLen: %d, data: %x, key: %s  count: %d\n", a1, m.state.PreimageOffset, datLen, dat[:datLen], m.state.PreimageKey, a2)
		alignment := a1 & 7
		space := 8 - alignment
		if space < datLen {
			datLen = space
		}
		if a2 < datLen {
			datLen = a2
		}
		var outMem [8]byte
		binary.BigEndian.PutUint64(outMem[:], mem)
		copy(outMem[alignment:], dat[:datLen])
		memory.SetDoubleWord(effAddr, binary.BigEndian.Uint64(outMem[:]))
		newPreimageOffset += datLen
		v0 = datLen
		//fmt.Printf("read %d pre-image bytes, new offset: %d, eff addr: %08x mem: %08x\n", datLen, m.state.PreimageOffset, effAddr, outMem)
	case FdHintRead: // hint response
		// don't actually read into memory, just say we read it all, we ignore the result anyway
		v0 = a2
	default:
		v0 = 0xFFffFFffFFffFFff
		v1 = MipsEBADF
	}

	return v0, v1, newPreimageOffset
}

func HandleSysWrite(a0, a1, a2 uint64, lastHint hexutil.Bytes, preimageKey [32]byte, preimageOffset uint64, oracle mipsevm.PreimageOracle, memory *memory.Memory, memTracker MemTracker, stdOut, stdErr io.Writer) (v0, v1 uint64, newLastHint hexutil.Bytes, newPreimageKey common.Hash, newPreimageOffset uint64) {
	// args: a0 = fd, a1 = addr, a2 = count
	// returns: v0 = written, v1 = err code
	v1 = uint64(0)
	newLastHint = lastHint
	newPreimageKey = preimageKey
	newPreimageOffset = preimageOffset

	switch a0 {
	case FdStdout:
		_, _ = io.Copy(stdOut, memory.ReadMemoryRange(a1, a2))
		v0 = a2
	case FdStderr:
		_, _ = io.Copy(stdErr, memory.ReadMemoryRange(a1, a2))
		v0 = a2
	case FdHintWrite:
		hintData, _ := io.ReadAll(memory.ReadMemoryRange(a1, a2))
		lastHint = append(lastHint, hintData...)
		for len(lastHint) >= 4 { // process while there is enough data to check if there are any hints
			hintLen := binary.BigEndian.Uint32(lastHint[:4])
			if hintLen <= uint32(len(lastHint[4:])) {
				hint := lastHint[4 : 4+hintLen] // without the length prefix
				lastHint = lastHint[4+hintLen:]
				oracle.Hint(hint)
			} else {
				break // stop processing hints if there is incomplete data buffered
			}
		}
		newLastHint = lastHint
		v0 = a2
	case FdPreimageWrite:
		effAddr := a1 & 0xFFFFFFFFFFFFFFF8
		memTracker.TrackMemAccess(effAddr)
		mem := memory.GetDoubleWord(effAddr)
		key := preimageKey
		alignment := a1 & 7
		space := 8 - alignment
		if space < a2 {
			a2 = space
		}
		copy(key[:], key[a2:])
		var tmp [8]byte
		binary.BigEndian.PutUint64(tmp[:], mem)
		copy(key[32-a2:], tmp[alignment:])
		newPreimageKey = key
		newPreimageOffset = 0
		//fmt.Printf("updating pre-image key: %s\n", m.state.PreimageKey)
		v0 = a2
	default:
		v0 = 0xFFffFFffFFffFFff
		v1 = MipsEBADF
	}

	return v0, v1, newLastHint, newPreimageKey, newPreimageOffset
}

func HandleSysFcntl(a0, a1 uint64) (v0, v1 uint64) {
	// args: a0 = fd, a1 = cmd
	v1 = uint64(0)

	if a1 == 3 { // F_GETFL: get file descriptor flags
		switch a0 {
		case FdStdin, FdPreimageRead, FdHintRead:
			v0 = 0 // O_RDONLY
		case FdStdout, FdStderr, FdPreimageWrite, FdHintWrite:
			v0 = 1 // O_WRONLY
		default:
			v0 = 0xFFffFFffFFffFFff
			v1 = MipsEBADF
		}
	} else {
		v0 = 0xFFffFFffFFffFFff
		v1 = MipsEINVAL // cmd not recognized by this kernel
	}

	return v0, v1
}

func HandleSyscallUpdates(cpu *mipsevm.CpuScalars, registers *[32]uint64, v0, v1 uint64) {
	registers[2] = v0
	registers[7] = v1

	cpu.PC = cpu.NextPC
	cpu.NextPC = cpu.NextPC + 4
}
