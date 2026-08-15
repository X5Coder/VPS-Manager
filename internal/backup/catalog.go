package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/store"
)

type cataloger struct {
	s             *Service
	gh            *GitHub
	work          string
	layout        *BackupLayout
	layerByDigest map[string]LayerLayout
	imageByKey    map[string]int
	containersDir string
	imagesDir     string
	releaseTag    string
	releaseSeq    int
	releaseAssets int
	volIdx        int
	volSize       int64
	volDir        string
	volRepo       string
	knownVolRepos map[string]bool
}

func (s *Service) newCataloger(gh *GitHub, work string, prev *BackupLayout) (*cataloger, error) {
	c := &cataloger{
		s: s, gh: gh, work: work,
		layerByDigest: map[string]LayerLayout{},
		imageByKey:    map[string]int{},
		releaseTag:    LayersRelease,
		volIdx:        1,
		knownVolRepos: map[string]bool{},
	}
	c.layout = &BackupLayout{
		ImagesRepo: ImagesRepo, ImagesRelease: LayersRelease,
		ContainersRepo: ContainersRepo, VolumeRepos: []string{},
	}
	if prev != nil {
		if prev.ImagesRelease != "" {
			c.layout.ImagesRelease = prev.ImagesRelease
			c.releaseTag = prev.ImagesRelease
		}
		c.layout.Layers = append([]LayerLayout{}, prev.Layers...)
		c.layout.Images = append([]ImageLayout{}, prev.Images...)
		c.layout.VolumeRepos = append([]string{}, prev.VolumeRepos...)
		for _, l := range c.layout.Layers {
			c.layerByDigest[l.Digest] = l
		}
		for i := range c.layout.Images {
			c.imageByKey[c.layout.Images[i].Key] = i
		}
		if n := len(c.layout.VolumeRepos); n > 0 {
			c.volRepo = c.layout.VolumeRepos[n-1]
			if strings.HasPrefix(c.volRepo, VolumeRepoPrefix+"-") {
				fmt.Sscanf(strings.TrimPrefix(c.volRepo, VolumeRepoPrefix+"-"), "%d", &c.volIdx)
			}
		}
	}
	if err := gh.EnsureRepo(ContainersRepo, "VPS MANAGE containers & room settings"); err != nil {
		return nil, err
	}
	if err := gh.EnsureRepo(ImagesRepo, "VPS MANAGE image map (layers live in Releases)"); err != nil {
		return nil, err
	}
	c.containersDir = filepath.Join(work, ContainersRepo)
	c.imagesDir = filepath.Join(work, ImagesRepo)
	if err := gh.CloneOrPull(ContainersRepo, c.containersDir); err != nil {
		_ = os.MkdirAll(c.containersDir, 0o750)
		_ = initGitRepo(c.containersDir, gh, ContainersRepo)
	}
	if err := gh.CloneOrPull(ImagesRepo, c.imagesDir); err != nil {
		_ = os.MkdirAll(c.imagesDir, 0o750)
		_ = initGitRepo(c.imagesDir, gh, ImagesRepo)
	}
	if _, err := os.Stat(filepath.Join(c.containersDir, ".git")); err != nil {
		_ = initGitRepo(c.containersDir, gh, ContainersRepo)
	}
	if _, err := os.Stat(filepath.Join(c.imagesDir, ".git")); err != nil {
		_ = initGitRepo(c.imagesDir, gh, ImagesRepo)
	}
	_ = os.WriteFile(filepath.Join(c.containersDir, "README.md"), []byte("# VPS MANAGE containers\n\nrooms/{short}/room.json + container configs + secrets.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(c.imagesDir, "README.md"), []byte("# VPS MANAGE images\n\nLayer blobs are GitHub Release assets (max 2GiB). See map.json.\n"), 0o644)
	if err := c.ensureVolumeRepo(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *cataloger) ensureVolumeRepo() error {
	if c.volRepo == "" {
		c.volRepo = VolumeRepoName(c.volIdx)
	}
	if err := c.gh.EnsureRepo(c.volRepo, "VPS MANAGE volumes (max 4GiB per repo)"); err != nil {
		return err
	}
	c.volDir = filepath.Join(c.work, c.volRepo)
	if err := c.gh.CloneOrPull(c.volRepo, c.volDir); err != nil {
		_ = os.MkdirAll(c.volDir, 0o750)
		_ = initGitRepo(c.volDir, c.gh, c.volRepo)
	}
	if _, err := os.Stat(filepath.Join(c.volDir, ".git")); err != nil {
		_ = initGitRepo(c.volDir, c.gh, c.volRepo)
	}
	_ = os.MkdirAll(filepath.Join(c.volDir, "chunks"), 0o750)
	if !c.knownVolRepos[c.volRepo] {
		c.knownVolRepos[c.volRepo] = true
		found := false
		for _, r := range c.layout.VolumeRepos {
			if r == c.volRepo {
				found = true
				break
			}
		}
		if !found {
			c.layout.VolumeRepos = append(c.layout.VolumeRepos, c.volRepo)
		}
	}
	c.volSize = dirSizeExcludingGit(c.volDir)
	return nil
}

func (c *cataloger) rotateVolumeRepo() error {
	if err := c.gh.CommitPush(c.volDir, "volumes "+c.volRepo); err != nil {
		c.s.report(-1, "volume repo push %s: %v", c.volRepo, err)
	}
	c.volIdx++
	c.volRepo = VolumeRepoName(c.volIdx)
	c.volSize = 0
	return c.ensureVolumeRepo()
}

func (c *cataloger) addRoom(room store.Room) (*ProjectMap, error) {
	short := store.ShortRoomID(room.ID)
	logicalDir := filepath.Join(c.work, "logical-"+short)
	if err := c.s.captureLogicalRoom(room, logicalDir); err != nil {
		return nil, err
	}
	roomPath := filepath.Join("rooms", short)
	dest := filepath.Join(c.containersDir, roomPath)
	_ = os.MkdirAll(dest, 0o750)
	ents, _ := os.ReadDir(logicalDir)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		src := filepath.Join(logicalDir, n)
		ln := strings.ToLower(n)
		if strings.Contains(ln, "-image-") || strings.Contains(ln, "-volume-") {
			continue
		}
		outName := n
		pref := "room-" + short + "-"
		if strings.HasPrefix(n, pref) {
			outName = strings.TrimPrefix(n, pref)
		}
		_ = copyFile(src, filepath.Join(dest, outName))
	}
	rl := RoomLayout{ID: room.ID, Name: room.Name, Short: short, Path: roomPath}

	cts, _ := c.s.Store.ListContainers(room.ID)
	seenImg := map[string]bool{}
	for _, ct := range cts {
		ref := strings.TrimSpace(ct.Image)
		if ref == "" || seenImg[ref] {
			continue
		}
		seenImg[ref] = true
		ord := ct.Ordinal
		if ord <= 0 {
			ord = 1
		}
		tar := filepath.Join(logicalDir, fmt.Sprintf("room-%s-image-%02d.tar", short, ord))
		if _, err := os.Stat(tar); err != nil {
			tar = tar + ".gz"
			if _, err := os.Stat(tar); err != nil {
				tar = ""
			}
		}
		key, err := c.addImage(room.ID, ref, tar)
		if err != nil {
			return nil, backupImageErr(ref, err)
		}
		if key != "" {
			rl.ImageKeys = append(rl.ImageKeys, key)
		}
	}

	vols, _ := c.s.Store.ListVolumes(room.ID)
	volByOrd := map[int]store.VolumeRec{}
	for _, v := range vols {
		volByOrd[v.Ordinal] = v
	}
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		if e.IsDir() || !strings.Contains(n, "-volume-") {
			continue
		}
		src := filepath.Join(logicalDir, e.Name())
		st, err := os.Stat(src)
		if err != nil {
			continue
		}
		ord := 1
		if i := strings.Index(n, "-volume-"); i >= 0 {
			fmt.Sscanf(n[i+len("-volume-"):], "%d", &ord)
		}
		dockerName := filepath.Base(e.Name())
		if v, ok := volByOrd[ord]; ok {
			dockerName = v.DockerName
			if dockerName == "" {
				dockerName = v.Name
			}
		}
		key := fmt.Sprintf("%s-volume-%02d", short, ord)
		sum, _, _ := HashFile(src)
		vl := VolumeLayout{Key: key, RoomID: room.ID, Name: dockerName, Size: st.Size(), SHA256: sum}
		parts, err := c.addLargeFile(src, "volumes/"+short+"/"+e.Name(), st.Size())
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", e.Name(), err)
		}
		vl.Parts = parts
		c.layout.Volumes = append(c.layout.Volumes, vl)
		rl.VolumeKeys = append(rl.VolumeKeys, key)
	}

	// runtime / vault: walk files. app / compose / bind: one tar each (not thousands of git blobs).
	roots := []rootSpec{
		{"fs/" + short + "/runtime", filepath.Join(c.s.RuntimeDir, room.ID)},
		{"fs/" + short + "/vault", filepath.Join(c.s.RoomsDir, room.ID)},
	}
	type treeRoot struct {
		prefix, root string
	}
	var trees []treeRoot
	projs, _ := c.s.Store.ListProjects(room.ID)
	for _, p := range projs {
		pdir := c.s.Rooms.ProjectDir(room.ID, p.ID)
		filesRoot, composeDir, _, binds := projects.ProjectLayout(pdir)
		if filesRoot != "" {
			trees = append(trees, treeRoot{"fs/" + short + "/app/" + p.ID, filesRoot})
		}
		if composeDir != "" && composeDir != filesRoot {
			trees = append(trees, treeRoot{"fs/" + short + "/compose/" + p.ID, composeDir})
		}
		for i, b := range binds {
			host, _, _ := projects.SplitBind(b)
			if host != "" {
				trees = append(trees, treeRoot{fmt.Sprintf("fs/%s/bind/%s/%02d", short, p.ID, i), host})
			}
		}
	}
	for _, tr := range trees {
		if err := c.addArchivedRoot(tr.root, tr.prefix); err != nil {
			return nil, err
		}
	}
	skipPG := hasGoodPostgresDumpForRoom(c.s, room.ID, projs)
	for _, root := range roots {
		walkRoot := root.root
		if real, err := filepath.EvalSymlinks(root.root); err == nil && real != "" {
			walkRoot = real
		}
		if err := filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if err := c.s.errIfStopped(); err != nil {
				return err
			}
			if info == nil {
				return nil
			}
			if info.IsDir() {
				base := strings.ToLower(info.Name())
				if base == "node_modules" || base == "site-packages" || base == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				st, e := os.Stat(path)
				if e != nil || st.IsDir() || !st.Mode().IsRegular() {
					return nil
				}
				info = st
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(walkRoot, path)
			rel = filepath.ToSlash(rel)
			if skipBackupRel(rel) || skipBackupFile(rel, info.Size()) {
				return nil
			}
			if skipPG && (strings.Contains(rel, "volumes/db/data") || strings.HasPrefix(rel, "volumes/db/data")) {
				return nil
			}
			logical := strings.Trim(root.prefix+"/"+rel, "/")
			if _, err := c.addLargeFile(path, logical, info.Size()); err != nil {
				return fmt.Errorf("file %s: %w", logical, err)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	if roomLooksPostgres(cts) && !skipPG && len(vols) > 0 && len(rl.VolumeKeys) == 0 {
		return nil, fmt.Errorf("postgres room %s has no SQL dump and no volume archive", room.Name)
	}

	c.layout.Rooms = append(c.layout.Rooms, rl)
	_ = c.gh.CommitPush(c.volDir, "room "+room.Name)
	pm := &ProjectMap{
		RoomID: room.ID, RoomName: room.Name, ProjectRepo: ContainersRepo,
		BackupRepos: c.layout.VolumeRepos, QuotaBytes: room.QuotaBytes,
		PassHash: room.PassHash, PassPlain: room.PassPlain, NetworkName: room.NetworkName,
	}
	for _, p := range projs {
		pm.Projects = append(pm.Projects, ProjectMeta{
			ID: p.ID, RoomID: p.RoomID, Name: p.Name, Image: p.Image,
			HostPort: p.HostPort, ContainerPort: p.ContainerPort,
			Domain: p.Domain, DomainEnabled: p.DomainEnabled,
			SSLStatus: p.SSLStatus, ExternalURL: p.ExternalURL,
			Status: p.Status, CreatedAt: p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			DeployKind: projects.DetectDeployKind(p, c.s.Rooms.ProjectDir(room.ID, p.ID)),
		})
		if pm.ProjectID == "" {
			pm.ProjectID, pm.ProjectName, pm.HostPort, pm.Domain, pm.Image = p.ID, p.Name, p.HostPort, p.Domain, p.Image
		}
	}
	return pm, nil
}

func (c *cataloger) addImage(roomID, ref, existingTar string) (string, error) {
	if c.s.Docker == nil || !c.s.Docker.Available() || strings.TrimSpace(ref) == "" {
		return "", nil
	}
	tarPath := existingTar
	if tarPath == "" {
		tarPath = filepath.Join(c.work, "img-"+slugFile(ref)+".tar")
		c.s.report(-1, "Saving image layers %s", ref)
		if err := c.s.Docker.SaveImagePlain(ref, tarPath); err != nil {
			gz := tarPath + ".gz"
			if err2 := c.s.Docker.SaveImage(ref, gz); err2 != nil {
				return "", err2
			}
			tarPath = gz
		}
	}
	unpacked := filepath.Join(c.work, "unpacked-"+slugFile(ref))
	_ = os.RemoveAll(unpacked)
	if err := unpackSaveArchive(tarPath, unpacked); err != nil {
		return "", err
	}
	meta, blobs, format, tags, err := splitImageTree(unpacked)
	if err != nil {
		return "", err
	}
	if len(tags) == 0 {
		tags = []string{ref}
	}
	keySum, _, _ := HashFile(tarPath)
	key := keySum
	if len(key) > 16 {
		key = key[:16]
	}
	if idx, ok := c.imageByKey[key]; ok {
		img := c.layout.Images[idx]
		img.RoomIDs = appendUnique(img.RoomIDs, roomID)
		c.layout.Images[idx] = img
		_ = os.RemoveAll(unpacked)
		return key, nil
	}
	treePath := filepath.Join("images", key, "tree")
	absTree := filepath.Join(c.imagesDir, treePath)
	_ = os.MkdirAll(absTree, 0o750)
	for _, m := range meta {
		src := filepath.Join(unpacked, filepath.FromSlash(m))
		dst := filepath.Join(absTree, filepath.FromSlash(m))
		_ = os.MkdirAll(filepath.Dir(dst), 0o750)
		_ = copyFile(src, dst)
	}
	var uses []ImageLayerUse
	for _, rel := range blobs {
		src := filepath.Join(unpacked, filepath.FromSlash(rel))
		sum, size, err := HashFile(src)
		if err != nil {
			return "", err
		}
		digest := "sha256:" + sum
		if _, ok := c.layerByDigest[digest]; !ok {
			lay, err := c.uploadLayer(digest, src, size)
			if err != nil {
				return "", err
			}
			c.layerByDigest[digest] = lay
			c.layout.Layers = append(c.layout.Layers, lay)
		}
		uses = append(uses, ImageLayerUse{Rel: rel, Digest: digest})
	}
	img := ImageLayout{Key: key, Tags: tags, RoomIDs: []string{roomID}, Format: format, TreePath: filepath.ToSlash(treePath), Layers: uses}
	c.imageByKey[key] = len(c.layout.Images)
	c.layout.Images = append(c.layout.Images, img)
	_ = os.RemoveAll(unpacked)
	if existingTar == "" {
		_ = os.Remove(tarPath)
	}
	return key, nil
}

func (c *cataloger) uploadLayer(digest, src string, size int64) (LayerLayout, error) {
	tag := c.releaseTag
	if c.releaseAssets > 800 {
		c.releaseSeq++
		tag = fmt.Sprintf("%s-%d", LayersRelease, c.releaseSeq)
		c.releaseTag = tag
		c.layout.ImagesRelease = tag
		c.releaseAssets = 0
	}
	if err := c.gh.EnsureRepo(ImagesRepo, "VPS MANAGE image map (layers live in Releases)"); err != nil {
		return LayerLayout{}, err
	}
	if _, err := c.gh.EnsureRelease(ImagesRepo, tag, "VPS image layers"); err != nil {
		return LayerLayout{}, err
	}
	base := LayerAssetName(digest)
	lay := LayerLayout{Digest: digest, Size: size, SHA256: strings.TrimPrefix(digest, "sha256:"), Release: tag}
	if size <= ReleaseAssetMax {
		c.s.report(-1, "Release upload %s (%.1f MiB)", base, float64(size)/1024/1024)
		if err := c.gh.UploadReleaseFile(ImagesRepo, tag, base, src); err != nil {
			return lay, err
		}
		lay.Assets = []string{base}
		c.releaseAssets++
		return lay, nil
	}
	tmp := filepath.Join(c.work, "relparts", base)
	_ = os.RemoveAll(tmp)
	parts, err := ChunkFileSize(src, tmp, base, ReleaseAssetMax)
	if err != nil {
		return lay, err
	}
	for _, p := range parts {
		c.s.report(-1, "Release part %s", p)
		if err := c.gh.UploadReleaseFile(ImagesRepo, tag, p, filepath.Join(tmp, p)); err != nil {
			return lay, err
		}
		lay.Assets = append(lay.Assets, p)
		c.releaseAssets++
	}
	_ = os.RemoveAll(tmp)
	return lay, nil
}

func (c *cataloger) addLargeFile(src, logical string, size int64) ([]VolumePart, error) {
	logical = strings.TrimPrefix(filepath.ToSlash(logical), "/")
	var parts []VolumePart
	if size <= MaxLogicalPart {
		p, err := c.putGitFile(src, logical, 0, size)
		if err != nil {
			return nil, err
		}
		return []VolumePart{p}, nil
	}
	tmp := filepath.Join(c.work, "logical-parts", slugFile(logical))
	_ = os.RemoveAll(tmp)
	names, err := ChunkFileSize(src, tmp, "L", MaxLogicalPart)
	if err != nil {
		return nil, err
	}
	for i, n := range names {
		lp := filepath.Join(tmp, n)
		st, _ := os.Stat(lp)
		sz := int64(0)
		if st != nil {
			sz = st.Size()
		}
		p, err := c.putGitFile(lp, fmt.Sprintf("%s.L%02d", logical, i), i, sz)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	_ = os.RemoveAll(tmp)
	return parts, nil
}

func (c *cataloger) putGitFile(src, logical string, index int, size int64) (VolumePart, error) {
	sum, _, err := HashFile(src)
	if err != nil {
		return VolumePart{}, err
	}
	need := size
	if need == 0 {
		st, _ := os.Stat(src)
		if st != nil {
			need = st.Size()
		}
	}
	if c.volSize+need > MaxRepoBytes && c.volSize > 0 {
		if err := c.rotateVolumeRepo(); err != nil {
			return VolumePart{}, err
		}
	}
	chunkDir := filepath.Join(c.volDir, "chunks")
	base := slugFile(logical)
	if len(base) > 80 {
		base = base[:80]
	}
	names, err := ChunkFile(src, chunkDir, fmt.Sprintf("%s-%s", base, sum[:12]))
	if err != nil {
		return VolumePart{}, err
	}
	p := VolumePart{Index: index, Size: need, SHA256: sum, Repo: c.volRepo}
	var added int64
	for _, n := range names {
		p.Chunks = append(p.Chunks, c.volRepo+":chunks/"+n)
		if st, e := os.Stat(filepath.Join(chunkDir, n)); e == nil {
			added += st.Size()
		}
	}
	c.volSize += added
	c.layout.Files = append(c.layout.Files, FileEntry{Path: logical, Size: need, SHA256: sum, Chunks: p.Chunks})
	if c.volSize >= MaxRepoBytes {
		if err := c.rotateVolumeRepo(); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (c *cataloger) finalize() error {
	mapBytes, _ := MarshalPretty(c.layout)
	_ = os.WriteFile(filepath.Join(c.imagesDir, "map.json"), mapBytes, 0o644)
	_ = os.WriteFile(filepath.Join(c.containersDir, "map.json"), mapBytes, 0o644)
	if err := c.gh.CommitPush(c.imagesDir, "image map"); err != nil {
		return fmt.Errorf("images repo: %w", err)
	}
	if err := c.gh.CommitPush(c.containersDir, "containers map"); err != nil {
		return fmt.Errorf("containers repo: %w", err)
	}
	if c.volDir != "" {
		_ = c.gh.CommitPush(c.volDir, "volumes finalize")
	}
	return nil
}

func dirSizeExcludingGit(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func slugFile(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "" {
		out = "file"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func (s *Service) restoreFromLayout(gh *GitHub, work string, man *Manifest) error {
	lay := man.Layout
	if lay == nil {
		return fmt.Errorf("backup map missing")
	}
	s.report(12, "Downloading container settings")
	cdir := filepath.Join(work, ContainersRepo)
	if err := gh.CloneOrPull(lay.ContainersRepo, cdir); err != nil {
		_ = gh.CloneOrPull(ContainersRepo, cdir)
	}
	idir := filepath.Join(work, ImagesRepo)
	_ = gh.CloneOrPull(lay.ImagesRepo, idir)
	if b, err := os.ReadFile(filepath.Join(idir, "map.json")); err == nil {
		var m BackupLayout
		if json.Unmarshal(b, &m) == nil && len(m.Layers) > 0 {
			lay = &m
		}
	}

	layerFiles := map[string]string{}
	s.report(20, "Downloading image layers from Releases")
	for i, layer := range lay.Layers {
		s.report(20+20*i/max(len(lay.Layers), 1), "Layer %s", layer.Digest[:min(len(layer.Digest), 18)])
		tag := layer.Release
		if tag == "" {
			tag = lay.ImagesRelease
		}
		if tag == "" {
			tag = LayersRelease
		}
		var locals []string
		for _, a := range layer.Assets {
			dest := filepath.Join(work, "rel", a)
			if err := gh.DownloadReleaseFile(lay.ImagesRepo, tag, a, dest); err != nil {
				return err
			}
			locals = append(locals, dest)
		}
		joined := filepath.Join(work, "layers", strings.ReplaceAll(layer.Digest, ":", "-"))
		if err := JoinChunks(locals, joined); err != nil {
			return err
		}
		if layer.SHA256 != "" {
			sum, _, _ := HashFile(joined)
			if sum != layer.SHA256 && sum != strings.TrimPrefix(layer.Digest, "sha256:") {
				return fmt.Errorf("layer checksum mismatch %s", layer.Digest)
			}
		}
		layerFiles[layer.Digest] = joined
	}

	s.report(42, "Loading Docker images")
	if len(lay.Images) > 0 && (s.Docker == nil || !s.Docker.Available()) {
		return fmt.Errorf("docker unavailable; cannot load backup images")
	}
	for _, img := range lay.Images {
		tree := filepath.Join(idir, filepath.FromSlash(img.TreePath))
		rebuild := filepath.Join(work, "rebuild-"+img.Key)
		_ = os.RemoveAll(rebuild)
		_ = copyDir(tree, rebuild)
		for _, u := range img.Layers {
			src := layerFiles[u.Digest]
			if src == "" {
				return fmt.Errorf("missing layer %s for image %s", u.Digest, img.Key)
			}
			dst := filepath.Join(rebuild, filepath.FromSlash(u.Rel))
			_ = os.MkdirAll(filepath.Dir(dst), 0o750)
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
		tarPath := filepath.Join(work, "load-"+img.Key+".tar")
		if err := packDirTar(rebuild, tarPath); err != nil {
			return err
		}
		if s.Docker != nil && s.Docker.Available() {
			s.report(-1, "docker load %s", strings.Join(img.Tags, ","))
			if err := s.Docker.LoadImage(tarPath); err != nil {
				return fmt.Errorf("docker load %s: %w", img.Key, err)
			}
			for _, tag := range img.Tags {
				if strings.TrimSpace(tag) == "" {
					continue
				}
				if !s.Docker.ImageExists(tag) {
					return fmt.Errorf("image %s is not on this VPS after docker load (no registry pull)", tag)
				}
			}
		}
		_ = os.Remove(tarPath)
		_ = os.RemoveAll(rebuild)
	}

	s.report(58, "Restoring rooms from container map")
	for _, rm := range lay.Rooms {
		roomJSON := filepath.Join(cdir, rm.Path, "room.json")
		if _, err := os.Stat(roomJSON); err != nil {
			roomJSON = filepath.Join(cdir, rm.Path, "room-"+rm.Short+"-room.json")
		}
		s.applyRoomJSON(roomJSON)
		if s.Rooms != nil {
			_ = s.Rooms.EnsureUnlocked(rm.ID)
		}
		network := ""
		if room, err := s.Store.GetRoom(rm.ID); err == nil && room != nil {
			network = room.NetworkName
		}
		if network == "" {
			var meta RoomBackupMeta
			if b, err := os.ReadFile(roomJSON); err == nil && json.Unmarshal(b, &meta) == nil {
				network = meta.NetworkName
			}
		}
		if s.Docker != nil && network != "" {
			if err := s.Docker.EnsureNetwork(network); err != nil {
				return fmt.Errorf("network %s: %w", network, err)
			}
		}
		backupDir := filepath.Join(s.RuntimeDir, rm.ID, "backup")
		_ = os.MkdirAll(backupDir, 0o750)
		if ents, err := os.ReadDir(filepath.Join(cdir, rm.Path)); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					continue
				}
				ln := strings.ToLower(e.Name())
				if strings.HasSuffix(ln, "-config.json") || strings.HasSuffix(ln, "-rw.tar.gz") || ln == "room.json" {
					_ = copyFile(filepath.Join(cdir, rm.Path, e.Name()), filepath.Join(backupDir, e.Name()))
				}
			}
		}
		if raw, err := os.ReadFile(filepath.Join(cdir, rm.Path, "secrets.enc")); err == nil {
			dest := filepath.Join(s.RoomsDir, rm.ID, "vault.bin")
			_ = os.MkdirAll(filepath.Dir(dest), 0o700)
			_ = os.WriteFile(dest, raw, 0o600)
		}
		if raw, err := os.ReadFile(filepath.Join(cdir, rm.Path, "secrets.env")); err == nil {
			dest := filepath.Join(s.RuntimeDir, rm.ID, ".env")
			_ = os.MkdirAll(filepath.Dir(dest), 0o700)
			_ = os.WriteFile(dest, raw, 0o600)
		}
	}

	s.report(70, "Restoring volumes")
	for _, vol := range lay.Volumes {
		var locals []string
		for _, p := range vol.Parts {
			for _, ref := range p.Chunks {
				repo, rel, ok := splitRef(ref)
				if !ok {
					continue
				}
				dest := filepath.Join(work, "chunks", repo, filepath.Base(rel))
				if _, err := os.Stat(dest); err != nil {
					if err := gh.DownloadFile(repo, rel, dest); err != nil {
						return err
					}
				}
				locals = append(locals, dest)
			}
		}
		joined := filepath.Join(work, "vol-"+vol.Key+".tar.gz")
		if err := JoinChunks(locals, joined); err != nil {
			return err
		}
		if vol.SHA256 != "" {
			sum, _, _ := HashFile(joined)
			if sum != vol.SHA256 {
				return fmt.Errorf("volume checksum mismatch %s", vol.Key)
			}
		}
		tmp, err := os.MkdirTemp("", "vm-rvol-*")
		if err != nil {
			return err
		}
		if err := extractTarGz(joined, tmp); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("volume extract %s: %w", vol.Name, err)
		}
		name := vol.Name
		if name == "" {
			os.RemoveAll(tmp)
			return fmt.Errorf("volume %s has no docker name", vol.Key)
		}
		s.report(-1, "Restoring volume %s", name)
		if strings.HasPrefix(name, "/") {
			if err := copyDir(tmp, name); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("volume %s: %w", name, err)
			}
		} else if s.Docker != nil {
			if err := s.Docker.CopyDirToVolume(tmp, name); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("volume %s: %w", name, err)
			}
		}
		os.RemoveAll(tmp)
		_ = os.Remove(joined)
	}

	if len(lay.Files) > 0 {
		s.report(82, "Restoring room files")
		byRoom := map[string][]FileEntry{}
		for _, fe := range lay.Files {
			p := filepath.ToSlash(fe.Path)
			if strings.HasPrefix(p, "fs/") {
				rest := strings.TrimPrefix(p, "fs/")
				short, _, _ := strings.Cut(rest, "/")
				byRoom[short] = append(byRoom[short], fe)
			}
		}
		for _, rm := range lay.Rooms {
			ents := byRoom[rm.Short]
			if len(ents) == 0 {
				continue
			}
			tmp := filepath.Join(work, "fs-"+rm.Short)
			if err := s.downloadEntries(gh, work, ents, tmp, "fs/"+rm.Short+"/"); err != nil {
				return err
			}
			_ = copyDir(filepath.Join(tmp, "runtime"), filepath.Join(s.RuntimeDir, rm.ID))
			_ = copyDir(filepath.Join(tmp, "vault"), filepath.Join(s.RoomsDir, rm.ID))
			if err := s.restoreArchivedTrees(rm.ID, tmp); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *cataloger) addArchivedRoot(src, logical string) error {
	if err := c.s.errIfStopped(); err != nil {
		return err
	}
	st, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		st, err = os.Stat(src)
		if err != nil {
			return nil
		}
	}
	logical = strings.Trim(logical, "/")
	if st.Mode().IsRegular() {
		_, err := c.addLargeFile(src, logical, st.Size())
		return err
	}
	if !st.IsDir() {
		return nil
	}
	dest := filepath.Join(c.work, "tree-"+slugFile(logical)+".tar.gz")
	c.s.report(-1, "Archiving %s", logical)
	if err := tarGzPath(src, dest); err != nil {
		return fmt.Errorf("%s: %w", logical, err)
	}
	defer os.Remove(dest)
	st2, err := os.Stat(dest)
	if err != nil {
		return err
	}
	_, err = c.addLargeFile(dest, logical+".tar.gz", st2.Size())
	return err
}

func (s *Service) restoreArchivedTrees(roomID, tmp string) error {
	if s.Rooms == nil || s.Store == nil {
		return nil
	}
	projs, _ := s.Store.ListProjects(roomID)
	for _, p := range projs {
		pdir := s.Rooms.ProjectDir(roomID, p.ID)
		filesRoot, composeDir, _, binds := projects.ProjectLayout(pdir)
		appTar := filepath.Join(tmp, "app", p.ID+".tar.gz")
		if filesRoot != "" {
			if err := extractIfExists(appTar, filesRoot); err != nil {
				return err
			}
		}
		if composeDir != "" {
			if err := extractIfExists(filepath.Join(tmp, "compose", p.ID+".tar.gz"), composeDir); err != nil {
				return err
			}
		}
		for i, b := range binds {
			host, _, _ := projects.SplitBind(b)
			if host == "" {
				continue
			}
			tar := filepath.Join(tmp, "bind", p.ID, fmt.Sprintf("%02d.tar.gz", i))
			if err := extractIfExists(tar, host); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractIfExists(tar, dest string) error {
	if dest == "" {
		return nil
	}
	if _, err := os.Stat(tar); err != nil {
		return nil
	}
	st, err := os.Stat(dest)
	if err == nil && !st.IsDir() {
		return extractTarGz(tar, filepath.Dir(dest))
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	return extractTarGz(tar, dest)
}

func copyDir(src, dest string) error {
	st, err := os.Stat(src)
	if err != nil || !st.IsDir() {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFile(path, target)
	})
}
