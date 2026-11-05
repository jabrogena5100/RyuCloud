package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/jobs"
	"github.com/jabrogena5100/RyuCloud/backend/internal/transcode"
)

// Progress budget (percent) per stage. Transcoding dominates, so it gets the widest band.
const (
	pctDownload  = 2
	pctProbe     = 12
	pctTranscode = 15 // start of the transcode band
	pctBandWidth = 60 // transcode band: 15..75
	pctThumbnail = 78
	pctUpload    = 82
)

// run does the actual work for one claimed job and returns what to persist. It never
// touches the job's database state: ProcessJob decides that from the returned error.
// Errors that retrying cannot fix are wrapped in *permanent.
func (w *Worker) run(ctx context.Context, job jobs.Job) (jobs.Result, error) {
	log := w.log.With("job_id", job.ID, "video_id", job.VideoID)
	progress := newReporter(w.store, job, log)

	dir, err := os.MkdirTemp(w.cfg.TempDir, "job-")
	if err != nil {
		return jobs.Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("could not remove temp dir", "dir", dir, "error", err)
		}
	}()

	// 1. Download the original from private storage.
	progress.set("downloading", pctDownload)
	src := filepath.Join(dir, "source")
	if err := w.download(ctx, job.SourceKey, src); err != nil {
		if errors.Is(err, integrations.ErrObjectNotFound) {
			return jobs.Result{}, &permanent{user: "The uploaded file could not be found. Please upload the video again.", err: err}
		}
		return jobs.Result{}, fmt.Errorf("download source: %w", err)
	}

	// 2. Validate and probe. A source that cannot be read will never succeed.
	progress.set("analyzing", pctProbe)
	probe, err := w.media.Probe(ctx, src)
	if err == nil {
		err = probe.Validate(w.cfg.MaxDuration)
	}
	if err != nil {
		return jobs.Result{}, classify(err)
	}
	log.Info("source probed", "width", probe.Width, "height", probe.Height,
		"duration_s", math.Round(probe.DurationSeconds*10)/10, "audio", probe.HasAudio, "codec", probe.VideoCodec)

	// 3. Transcode each rendition (never above the source's own height).
	plan := transcode.Plan(probe.Width, probe.Height)
	hlsDir := filepath.Join(dir, "hls")
	var renditions []jobs.Rendition
	var variants []transcode.Variant
	for i, r := range plan {
		base := pctTranscode + pctBandWidth*i/len(plan)
		span := float64(pctBandWidth) / float64(len(plan))
		stage := "transcoding " + r.Name
		progress.set(stage, base)
		out := filepath.Join(hlsDir, r.Name)
		err := w.media.Transcode(ctx, transcode.Request{
			Source:    src,
			OutDir:    out,
			Rendition: r,
			Duration:  probe.DurationSeconds,
			OnProgress: func(f float64) {
				progress.set(stage, base+int(f*span))
			},
		})
		if err != nil {
			return jobs.Result{}, classify(fmt.Errorf("transcode %s: %w", r.Name, err))
		}
		meas, err := measureRendition(out)
		if err != nil {
			return jobs.Result{}, fmt.Errorf("check %s output: %w", r.Name, err)
		}
		rel := r.Name + "/index.m3u8"
		renditions = append(renditions, jobs.Rendition{
			Name: r.Name, Width: r.Width, Height: r.Height,
			BandwidthBPS: meas.peakBPS, SegmentCount: meas.segments, SizeBytes: meas.bytes,
			PlaylistKey: integrations.HLSKey(job.VideoID, rel),
		})
		variants = append(variants, transcode.Variant{
			Playlist: rel, Width: r.Width, Height: r.Height, Bandwidth: meas.peakBPS, AvgBandwidth: meas.avgBPS,
		})
	}
	if err := os.WriteFile(filepath.Join(hlsDir, "master.m3u8"), []byte(transcode.MasterPlaylist(variants)), 0o644); err != nil {
		return jobs.Result{}, fmt.Errorf("write master playlist: %w", err)
	}

	// 4. Thumbnail. A missing poster frame is not worth failing a playable video for.
	progress.set("thumbnail", pctThumbnail)
	thumb := filepath.Join(dir, "thumbnail.jpg")
	hasThumb := true
	if err := w.media.Thumbnail(ctx, src, thumb, transcode.ThumbnailTime(probe.DurationSeconds)); err != nil {
		if ctx.Err() != nil {
			return jobs.Result{}, ctx.Err()
		}
		log.Warn("thumbnail failed; retrying from the first frame", "error", err)
		if err := w.media.Thumbnail(ctx, src, thumb, 0); err != nil {
			if ctx.Err() != nil {
				return jobs.Result{}, ctx.Err()
			}
			log.Warn("thumbnail failed; continuing without one", "error", err)
			hasThumb = false
		}
	}

	// 5. Upload. Clear anything a previous attempt left, then write segments and media
	//    playlists, then the master playlist and thumbnail.
	progress.set("uploading", pctUpload)
	if _, err := w.storage.DeletePrefix(ctx, integrations.VideoPrefix(job.VideoID)); err != nil {
		return jobs.Result{}, fmt.Errorf("clear previous output: %w", err)
	}
	if err := w.uploadTree(ctx, job.VideoID, hlsDir); err != nil {
		return jobs.Result{}, err
	}
	res := jobs.Result{
		DurationSeconds: int(math.Round(probe.DurationSeconds)),
		ManifestKey:     integrations.HLSMasterKey(job.VideoID),
		Renditions:      renditions,
	}
	if hasThumb {
		key := integrations.ThumbnailKey(job.VideoID)
		if err := w.putFile(ctx, key, thumb, "image/jpeg"); err != nil {
			return jobs.Result{}, err
		}
		res.ThumbnailKey = key
	}
	return res, nil
}

// classify turns a media error into a permanent failure when retrying cannot help.
func classify(err error) error {
	var invalid *transcode.InvalidMediaError
	if errors.As(err, &invalid) {
		return &permanent{
			user: "This video couldn't be processed: " + invalid.Reason + ".",
			err:  err,
		}
	}
	return err
}

func (w *Worker) download(ctx context.Context, key, dst string) error {
	obj, err := w.storage.GetObject(ctx, key)
	if err != nil {
		return err
	}
	defer obj.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, obj.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// uploadTree uploads hlsDir to storage: media files first, master playlist last, so a
// reader that sees the master can rely on everything it references already being there.
func (w *Worker) uploadTree(ctx context.Context, videoID, hlsDir string) error {
	var files []string
	err := filepath.WalkDir(hlsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(hlsDir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fmt.Errorf("list output: %w", err)
	}
	sort.SliceStable(files, func(i, j int) bool { return rank(files[i]) < rank(files[j]) })
	for _, rel := range files {
		if err := w.putFile(ctx, integrations.HLSKey(videoID, rel), filepath.Join(hlsDir, filepath.FromSlash(rel)), contentTypeFor(rel)); err != nil {
			return err
		}
	}
	return nil
}

// rank orders uploads: segments, then media playlists, then the master playlist.
func rank(rel string) int {
	switch {
	case rel == "master.m3u8":
		return 2
	case strings.HasSuffix(rel, ".m3u8"):
		return 1
	}
	return 0
}

func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return "application/octet-stream"
}

func (w *Worker) putFile(ctx context.Context, key, path, contentType string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := w.storage.PutObject(ctx, key, contentType, f, info.Size()); err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}

// measurement summarises one finished rendition directory.
type measurement struct {
	segments int
	bytes    int64
	peakBPS  int64 // highest segment bitrate: the HLS BANDWIDTH
	avgBPS   int64
}

// measureRendition checks that ffmpeg really finished (complete playlist, every
// segment present and non-empty) and measures bitrates from the actual segment sizes.
func measureRendition(dir string) (measurement, error) {
	text, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		return measurement{}, err
	}
	segs, ended := transcode.ParseMediaPlaylist(string(text))
	if !ended || len(segs) == 0 {
		return measurement{}, errors.New("the playlist is incomplete or has no segments")
	}

	m := measurement{segments: len(segs), bytes: int64(len(text))}
	var totalSeconds float64
	var segBytes int64
	for _, s := range segs {
		info, err := os.Stat(filepath.Join(dir, filepath.Base(s.Name)))
		if err != nil {
			return measurement{}, err
		}
		if info.Size() == 0 {
			return measurement{}, fmt.Errorf("segment %s is empty", s.Name)
		}
		segBytes += info.Size()
		totalSeconds += s.Duration
		if s.Duration > 0 {
			if bps := int64(float64(info.Size()) * 8 / s.Duration); bps > m.peakBPS {
				m.peakBPS = bps
			}
		}
	}
	m.bytes += segBytes
	if totalSeconds > 0 {
		m.avgBPS = int64(float64(segBytes) * 8 / totalSeconds)
	}
	if m.peakBPS < m.avgBPS {
		m.peakBPS = m.avgBPS
	}
	return m, nil
}

// reporter writes progress to the job row, but at most about once a second and only
// when the percentage or stage moved, so a chatty ffmpeg does not hammer PostgreSQL.
type reporter struct {
	store *jobs.Store
	job   jobs.Job
	log   *slog.Logger

	mu    sync.Mutex
	last  time.Time
	stage string
	pct   int
}

func newReporter(store *jobs.Store, job jobs.Job, log *slog.Logger) *reporter {
	return &reporter{store: store, job: job, log: log, pct: -1}
}

func (r *reporter) set(stage string, pct int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pct > 99 {
		pct = 99
	}
	stageChanged := stage != r.stage
	if !stageChanged && (pct <= r.pct || time.Since(r.last) < time.Second) {
		return
	}
	r.stage, r.pct, r.last = stage, pct, time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.store.SetProgress(ctx, r.job, stage, pct); err != nil && !errors.Is(err, jobs.ErrLeaseLost) {
		r.log.Debug("could not record progress", "error", err)
	}
}
