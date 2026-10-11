package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const (
	loggerINFO  = "INFO"
	loggerWarn  = "WARN"
	loggerError = "ERR"
	loggerDebug = "DEBUG"
)

const maxLogCount = 1000000

var logCount atomic.Int64
var setupLogLock sync.Mutex
var setupLogWorking atomic.Bool
var currentLogPath string
var currentLogPathMu sync.RWMutex
var currentLogFile *os.File
var maxLogSizeMB atomic.Int64

func init() {
	maxLogSizeMB.Store(100)
}

func GetMaxLogSizeMB() int {
	v := maxLogSizeMB.Load()
	if v <= 0 {
		if common.LogMaxSizeMB != nil && *common.LogMaxSizeMB > 0 {
			return *common.LogMaxSizeMB
		}
		return 100
	}
	return int(v)
}

func SetMaxLogSizeMB(mb int) {
	if mb <= 0 {
		mb = 100
	}
	maxLogSizeMB.Store(int64(mb))
	if common.LogMaxSizeMB != nil {
		*common.LogMaxSizeMB = mb
	}
	gopool.Go(func() {
		_, _, _ = PruneLogFiles()
	})
}

func GetCurrentLogPath() string {
	currentLogPathMu.RLock()
	defer currentLogPathMu.RUnlock()
	return currentLogPath
}

type logFileMeta struct {
	path    string
	size    int64
	modTime time.Time
}

var pruneLock sync.Mutex

// PruneLogFiles checks all oneapi-*.log files in common.LogDir and removes oldest
// inactive files if total size exceeds maxLogSizeMB (default 100MB).
// The currently active log file is NEVER deleted.
func PruneLogFiles() (deletedCount int, freedBytes int64, err error) {
	if !pruneLock.TryLock() {
		return 0, 0, nil
	}
	defer pruneLock.Unlock()

	if common.LogDir == nil || *common.LogDir == "" {
		return 0, 0, nil
	}
	entries, err := os.ReadDir(*common.LogDir)
	if err != nil {
		return 0, 0, err
	}

	activeLog := GetCurrentLogPath()
	var files []logFileMeta
	var totalSize int64

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "oneapi-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(*common.LogDir, name)
		files = append(files, logFileMeta{
			path:    p,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		totalSize += info.Size()
	}

	maxAllowedBytes := int64(GetMaxLogSizeMB()) * 1024 * 1024
	if totalSize <= maxAllowedBytes {
		return 0, 0, nil
	}

	// Sort files by modification time ascending (oldest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	for _, f := range files {
		if f.path == activeLog {
			continue // Preserve currently open active log file
		}
		if totalSize <= maxAllowedBytes {
			break
		}
		if removeErr := os.Remove(f.path); removeErr == nil {
			deletedCount++
			freedBytes += f.size
			totalSize -= f.size
		}
	}
	return deletedCount, freedBytes, nil
}

// StartLogCleaner launches a background maintenance ticker that runs every 5 minutes:
// prunes stale log files exceeding the size limit and reclaims runtime OS memory.
func StartLogCleaner(ctx context.Context) {
	gopool.Go(func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _, _ = PruneLogFiles()
				runtime.GC()
				debug.FreeOSMemory()
			}
		}
	})
}

func SetupLogger() {
	defer func() {
		setupLogWorking.Store(false)
	}()
	if *common.LogDir != "" {
		ok := setupLogLock.TryLock()
		if !ok {
			log.Println("setup log is already working")
			return
		}
		defer func() {
			setupLogLock.Unlock()
		}()
		logPath := filepath.Join(*common.LogDir, fmt.Sprintf("oneapi-%s.log", time.Now().Format("20060102150405")))
		fd, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal("failed to open log file")
		}
		currentLogPathMu.Lock()
		oldFile := currentLogFile
		currentLogPath = logPath
		currentLogFile = fd
		currentLogPathMu.Unlock()

		common.LogWriterMu.Lock()
		gin.DefaultWriter = io.MultiWriter(os.Stdout, fd)
		gin.DefaultErrorWriter = io.MultiWriter(os.Stderr, fd)
		if oldFile != nil {
			_ = oldFile.Close()
		}
		common.LogWriterMu.Unlock()

		gopool.Go(func() {
			_, _, _ = PruneLogFiles()
		})
	}
}

func LogInfo(ctx context.Context, msg string) {
	logHelper(ctx, loggerINFO, msg)
}

func LogWarn(ctx context.Context, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	logHelper(ctx, loggerWarn, msg)
}

func LogError(ctx context.Context, msg string) {
	logHelper(ctx, loggerError, msg)
}

func LogDebug(ctx context.Context, msg string, args ...any) {
	if common.DebugEnabled {
		if len(args) > 0 {
			msg = fmt.Sprintf(msg, args...)
		}
		logHelper(ctx, loggerDebug, msg)
	}
}

func logHelper(ctx context.Context, level string, msg string) {
	var id any = "SYSTEM"
	if ctx != nil {
		if requestID := ctx.Value(common.RequestIdKey); requestID != nil {
			id = requestID
		}
	}
	now := time.Now()
	common.LogWriterMu.RLock()
	writer := gin.DefaultErrorWriter
	if level == loggerINFO {
		writer = gin.DefaultWriter
	}
	_, _ = fmt.Fprintf(writer, "[%s] %v | %s | %s \n", level, now.Format("2006/01/02 - 15:04:05"), id, msg)
	common.LogWriterMu.RUnlock()

	count := logCount.Add(1)
	shouldRotate := false
	if count > maxLogCount {
		shouldRotate = true
	} else if count%1000 == 0 {
		currentLogPathMu.RLock()
		f := currentLogFile
		currentLogPathMu.RUnlock()
		if f != nil {
			if fi, err := f.Stat(); err == nil {
				maxBytes := int64(GetMaxLogSizeMB()) * 1024 * 1024
				if fi.Size() >= maxBytes {
					shouldRotate = true
				}
			}
		}
	}

	if shouldRotate && setupLogWorking.CompareAndSwap(false, true) {
		logCount.Store(0)
		gopool.Go(func() {
			SetupLogger()
		})
	}
}

func LogQuota(quota int) string {
	// 新逻辑：根据额度展示类型输出
	q := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		usd := q / common.QuotaPerUnit
		cny := usd * operation_setting.USDExchangeRate
		return fmt.Sprintf("¥%.6f 额度", cny)
	case operation_setting.QuotaDisplayTypeCustom:
		usd := q / common.QuotaPerUnit
		rate := operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
		symbol := operation_setting.GetGeneralSetting().CustomCurrencySymbol
		if symbol == "" {
			symbol = "¤"
		}
		if rate <= 0 {
			rate = 1
		}
		v := usd * rate
		return fmt.Sprintf("%s%.6f 额度", symbol, v)
	case operation_setting.QuotaDisplayTypeTokens:
		return fmt.Sprintf("%d 点额度", quota)
	default: // USD
		return fmt.Sprintf("＄%.6f 额度", q/common.QuotaPerUnit)
	}
}

func FormatQuota(quota int) string {
	q := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		usd := q / common.QuotaPerUnit
		cny := usd * operation_setting.USDExchangeRate
		return fmt.Sprintf("¥%.6f", cny)
	case operation_setting.QuotaDisplayTypeCustom:
		usd := q / common.QuotaPerUnit
		rate := operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
		symbol := operation_setting.GetGeneralSetting().CustomCurrencySymbol
		if symbol == "" {
			symbol = "¤"
		}
		if rate <= 0 {
			rate = 1
		}
		v := usd * rate
		return fmt.Sprintf("%s%.6f", symbol, v)
	case operation_setting.QuotaDisplayTypeTokens:
		return fmt.Sprintf("%d", quota)
	default:
		return fmt.Sprintf("＄%.6f", q/common.QuotaPerUnit)
	}
}

// LogJson 仅供测试使用 only for test
func LogJson(ctx context.Context, msg string, obj any) {
	if !common.DebugEnabled {
		return
	}
	jsonStr, err := common.Marshal(obj)
	if err != nil {
		LogError(ctx, fmt.Sprintf("json marshal failed: %s", err.Error()))
		return
	}
	LogDebug(ctx, "%s | %s", msg, jsonStr)
}
