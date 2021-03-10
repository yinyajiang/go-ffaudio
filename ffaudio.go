package ffaudio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	tools "github.com/yinyajiang/go-ytools/utils"
	"github.com/yinyajiang/portaudio/simpleutil"
)

//ViceFile //副音频文件
type ViceFile struct {
	Path       string
	StartLocal int //在主音频中的起始位置，ms
}

//FadeArg //淡入淡出参数
type FadeArg struct {
	StartLocal int //起始位置,ms
	Duration   int //时长,ms
}

//FFmpegAudioOperation ...
type FFmpegAudioOperation struct {
	ffmpegProcess map[int64][]*exec.Cmd
	sync.Mutex
}

//NewFFOperation ...
func NewFFOperation() *FFmpegAudioOperation {
	return &FFmpegAudioOperation{
		ffmpegProcess: make(map[int64][]*exec.Cmd, 0),
	}
}

//TranscodeAnyToWav 转音频、视频为wav
func (ff *FFmpegAudioOperation) TranscodeAnyToWav(ctx context.Context, inp, outp string) (err error) {
	//ffmpeg -i p -vn -c:a pcm_s16le  output.wav
	os.Remove(outp)
	opid, err := ff.startOperation("./ffmpeg", "-loglevel", "error", "-i", inp, "-vn", "-c:a", "pcm_s16le", "-ac", "2", outp)
	if err != nil {
		return
	}
	return ff.contextWaitOperation(ctx, opid)
}

//RecordAudio  录音
func (ff *FFmpegAudioOperation) RecordAudio(ctx context.Context, outp string) (real string, recordDur int64, err error) {
	real = outp
	//if runtime.GOOS == "windows" {
	//优先使用portaudio
	var f *os.File

	if path.Ext(real) != ".aiff" {
		real += ".aiff"
	}
	f, err = tools.CreateFile(real)
	if err != nil {
		//goto portaudio
		return
	}
	defer f.Close()
	recordDur, err = simpleutil.RecordAiff(ctx, f)
	return

	//}
	//portaudio:
	//	err = ff.ffrecordAudio(ctx, outp)
	return
}

//Cut 剪切,ms
func (ff *FFmpegAudioOperation) Cut(ctx context.Context, inp string, start, len int, outp string) (err error) {
	//ffmpeg -i p  -ss start -t len -c copy outp
	os.Remove(outp)
	opid, err := ff.startOperation("./ffmpeg", "-loglevel", "error", "-i", inp, "-ss", fmt.Sprintf("%.1f", float64(start)/1000), "-t", fmt.Sprintf("%.1f", float64(len)/1000),
		"-c", "copy", "-loglevel", "error", outp)

	return ff.contextWaitOperation(ctx, opid)
}

//AudioMix ...
func (ff *FFmpegAudioOperation) AudioMix(ctx context.Context, mainPath string, files []ViceFile, volumePercent float64, fadein, fadeout *FadeArg, outp string) (err error) {
	if len(files) == 0 && volumePercent == 1.0 && fadein == nil && fadeout == nil {
		//copyfile
		return tools.CopyFile(ctx, mainPath, outp)
	}
	args := makeAMixArgs(mainPath, files, volumePercent, fadein, fadeout)
	args = append(args, outp)

	os.Remove(outp)
	opid, err := ff.startOperation("./ffmpeg", args...)
	return ff.contextWaitOperation(ctx, opid)
}

//PlaySlice ...
func (ff *FFmpegAudioOperation) PlaySlice(ctx context.Context, p string, start, duration int) (err error) {
	opid, err := ff.startOperation("./ffplay", "-i", p, "-ss", fmt.Sprintf("%.1f", float64(start)/1000), "-t", fmt.Sprintf("%.1f", float64(duration)/1000), "-autoexit", "-nodisp", "-loglevel", "error")
	return ff.contextWaitOperation(ctx, opid)
}

//PlayFull ...
func (ff *FFmpegAudioOperation) PlayFull(ctx context.Context, p string) (err error) {
	opid, err := ff.startOperation("./ffplay", "-i", p, "-autoexit", "-nodisp", "-loglevel", "error")
	return ff.contextWaitOperation(ctx, opid)
}

//PreviewAMix ...
func (ff *FFmpegAudioOperation) PreviewAMix(ctx context.Context, mainPath string, files []ViceFile, volumePercent float64, fadein, fadeout *FadeArg) (err error) {
	if len(files) == 0 && volumePercent == 1.0 && fadein == nil && fadeout == nil {
		return ff.PlayFull(ctx, mainPath)
	}

	args := makeAMixArgs(mainPath, files, volumePercent, fadein, fadeout)
	args = append(args, "-f", "wav", "-") //输出到标准输出
	gen := exec.Command("./ffmpeg", args...)
	gen.Stderr = os.Stderr

	play := exec.Command("./ffplay", "-i", "-", "-autoexit", "-nodisp", "-loglevel", "error") //从标准输入读取
	play.Stderr = os.Stderr
	genPipe, err := gen.StdoutPipe() //从ffmpeg 的标准输出获取流
	if err != nil {
		return
	}
	play.Stdin = genPipe

	ch := make(chan struct{}, 1)
	var genErr error
	go func() {
		genErr = gen.Start()
		ch <- struct{}{}
	}()
	<-ch
	err = play.Start()
	if err == nil && genErr == nil {
		opid := ff.addOp(0, gen)
		opid = ff.addOp(opid, play)
		return ff.contextWaitOperation(ctx, opid)
	} else if err == nil {
		err = genErr
	}
	return
}

func (ff *FFmpegAudioOperation) ffrecordAudio(ctx context.Context, outp string) (err error) {
	os.Remove(outp)
	defname, err := simpleutil.GetDefaultInputDeviceName()
	if err != nil {
		return
	}

	capArg := []string{}
	if runtime.GOOS == "darwin" {
		//-f avfoundation  -i ":defname"
		capArg = append(capArg, "-f", "avfoundation", "-i", `:`+defname)
	} else {
		// -f dshow -i audio="defname"
		capArg = append(capArg, "-f", "dshow", "-i", "audio="+defname)
	}
	capArg = append(capArg, "-c:a", "pcm_s16le", "-ac", "2", "-loglevel", "error", outp)
	fmt.Println(strings.Join(capArg, " "))
	opid, err := ff.startOperation("./ffmpeg", capArg...)
	if err != nil {
		return
	}
	return ff.contextWaitOperation(ctx, opid)
}

func (ff *FFmpegAudioOperation) contextWaitOperation(ctx context.Context, opid int64) (err error) {
	ch := make(chan struct{}, 1)
	go func() {
		select {
		case <-ctx.Done():
			ff.terminateOperation(opid)
		case <-ch:
			break
		}
	}()
	err = ff.waitOperation(opid)
	ch <- struct{}{}
	return err
}

//WaitOperation ...
func (ff *FFmpegAudioOperation) waitOperation(opid int64) (err error) {
	ops, err := ff.getOps(opid)
	if err != nil {
		return
	}
	c := ops[0]
	err = c.Wait()
	ff.delOps(opid)
	return
}

//TerminateOperation ...
func (ff *FFmpegAudioOperation) terminateOperation(opid int64) (err error) {
	ops, err := ff.getOps(opid)
	if err != nil {
		return
	}
	for _, c := range ops {
		err = c.Process.Kill()
		c.Process.Release()
	}
	ff.delOps(opid)
	return
}

func (ff *FFmpegAudioOperation) startOperation(cmd string, arg ...string) (opid int64, err error) {
	c := exec.Command(cmd, arg...)
	c.Stderr = os.Stderr
	err = c.Start()
	if err == nil {
		opid = ff.addOp(0, c)
	}
	return
}

func (ff *FFmpegAudioOperation) addOp(opid int64, c *exec.Cmd) int64 {
	ff.Lock()
	defer ff.Unlock()
	if opid <= 0 {
		opid = time.Now().UnixNano()
	}

	ops, ok := ff.ffmpegProcess[opid]
	if !ok {
		ops = make([]*exec.Cmd, 0, 2)
	}
	ops = append(ops, c)
	ff.ffmpegProcess[opid] = ops
	return opid
}

func (ff *FFmpegAudioOperation) getOps(opid int64) (ops []*exec.Cmd, err error) {
	ff.Lock()
	defer ff.Unlock()
	ops, ok := ff.ffmpegProcess[opid]
	if !ok {
		err = fmt.Errorf("Not find operation")
		return
	}
	return
}

func (ff *FFmpegAudioOperation) delOps(opid int64) {
	ff.Lock()
	delete(ff.ffmpegProcess, opid)
	ff.Unlock()
}

func makeAMixArgs(mainPath string, files []ViceFile, volumePercent float64, fadein, fadeout *FadeArg) (args []string) {
	//-i main -i file1 -i file2...
	//-filter_complex "[1]adelay=start1|start1[del1],[2]adelay=start2|start2[del2]...,[0][del1][del2]...amix=inputs=3:duration=first[out],[out]afade=t=in:ss=fadeinStart:d=fadeinDur[out],[out]afade=t=out:st=fadeoutStart:d=fadeoutDur[out],[out]volume=volumePercent" out.wsv

	//文件列表
	args = make([]string, 0, 2)
	args = append(args, "-i", mainPath)
	for _, elem := range files {
		args = append(args, "-i", elem.Path)
	}

	//过滤参数
	args = append(args, "-filter_complex")
	fileterArg := ""
	isOut := false
	if len(files) > 0 {
		for i, elem := range files {
			if len(fileterArg) > 0 {
				fileterArg += ","
			}
			fileterArg += fmt.Sprintf("[%d]adelay=%d|%d[del%d]", i+1, elem.StartLocal, elem.StartLocal, i+1)
		}
		fileterArg += ",[0]"
		for i := range files {
			fileterArg += fmt.Sprintf("[del%d]", i+1)
		}
		fileterArg += fmt.Sprintf("amix=inputs=%d:duration=first[out]", len(files)+1)
		isOut = true
	}

	if fadein != nil {
		if isOut {
			fileterArg += ",[out]"
		} else {
			fileterArg = "[0]"
		}
		fileterArg += fmt.Sprintf("afade=t=in:ss=%.1f:d=%.1f[out]", float64(fadein.StartLocal)/1000, float64(fadein.Duration)/1000)
		isOut = true
	}
	if fadeout != nil {
		if isOut {
			fileterArg += ",[out]"
		} else {
			fileterArg = "[0]"
		}
		fileterArg += fmt.Sprintf("afade=t=out:st=%.1f:d=%.1f[out]", float64(fadeout.StartLocal)/1000, float64(fadeout.Duration)/1000)
		isOut = true
	}
	if volumePercent != 1.0 {
		if isOut {
			fileterArg += ",[out]"
		} else {
			fileterArg += "[0]"
		}
		fileterArg += fmt.Sprintf("volume=%.1f[out]", volumePercent)
		isOut = true
	}

	if isOut {
		fileterArg = fileterArg[0 : len(fileterArg)-len("[out]")]
	}

	args = append(args, fileterArg, "-ac", "2", "-ar", "44100", "-loglevel", "error")
	fmt.Println(fileterArg)
	return
}
