package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ghRelease struct {
	ID        int64  `json:"id"`
	Tag       string `json:"tag_name"`
	UploadURL string `json:"upload_url"`
}

type ghAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (g *GitHub) longHTTP() *http.Client {
	return &http.Client{Timeout: 3 * time.Hour}
}

func (g *GitHub) EnsureRelease(repo, tag, name string) (*ghRelease, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+g.User+"/"+repo+"/releases/tags/"+url.PathEscape(tag), nil)
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode == 200 {
		var rel ghRelease
		if err := json.Unmarshal(body, &rel); err != nil {
			return nil, err
		}
		return &rel, nil
	}
	payload, _ := json.Marshal(map[string]any{
		"tag_name": tag, "name": name, "body": "VPS MANAGE image layers (do not edit assets by hand).",
	})
	req, _ = http.NewRequest("POST", "https://api.github.com/repos/"+g.User+"/"+repo+"/releases", bytes.NewReader(payload))
	g.auth(req)
	req.Header.Set("Content-Type", "application/json")
	res, err = g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("create release %s/%s: %s", repo, tag, truncate(string(body), 240))
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (g *GitHub) ListReleaseAssets(repo, tag string) ([]ghAsset, error) {
	rel, err := g.EnsureRelease(repo, tag, tag)
	if err != nil {
		return nil, err
	}
	var all []ghAsset
	for page := 1; page <= 20; page++ {
		u := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/%d/assets?per_page=100&page=%d", g.User, repo, rel.ID, page)
		req, _ := http.NewRequest("GET", u, nil)
		g.auth(req)
		res, err := g.Client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode >= 300 {
			return nil, fmt.Errorf("list assets %s: %s", tag, truncate(string(body), 200))
		}
		var batch []ghAsset
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

func (g *GitHub) UploadReleaseFile(repo, tag, assetName, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	assets, err := g.ListReleaseAssets(repo, tag)
	if err != nil {
		return err
	}
	for _, a := range assets {
		if a.Name == assetName && a.Size == st.Size() {
			return nil
		}
	}
	rel, err := g.EnsureRelease(repo, tag, tag)
	if err != nil {
		return err
	}
	base := strings.Split(rel.UploadURL, "{")[0]
	u := base + "?name=" + url.QueryEscape(assetName)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequest("POST", u, f)
	if err != nil {
		return err
	}
	g.auth(req)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = st.Size()
	res, err := g.longHTTP().Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("upload asset %s: %s", assetName, truncate(string(body), 240))
	}
	return nil
}

func (g *GitHub) DownloadReleaseFile(repo, tag, assetName, dest string) error {
	assets, err := g.ListReleaseAssets(repo, tag)
	if err != nil {
		return err
	}
	var id int64
	for _, a := range assets {
		if a.Name == assetName {
			id = a.ID
			break
		}
	}
	if id == 0 {
		return fmt.Errorf("release asset %s/%s/%s not found", repo, tag, assetName)
	}
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/assets/%d", g.User, repo, id)
	req, _ := http.NewRequest("GET", u, nil)
	g.auth(req)
	req.Header.Set("Accept", "application/octet-stream")
	res, err := g.longHTTP().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("download asset %s: %s", assetName, truncate(string(body), 200))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}
