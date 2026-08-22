package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Id allocation is Jira/Linear-style: one monotone counter per artifact kind,
// minted by the engine at write time and never reused — even after a file is
// deleted. The minted id is the maximum of the persisted counter and the
// highest id already on disk, so a fresh checkout (counters reset) cannot
// collide with committed artifacts, and a delete cannot recycle an id.
//
// The guarantee is process-wide, not caller-wide: every mint holds the store
// lock (flock on data/.idlock), so multiple devtop processes on the same
// store cannot hand out the same id — an in-process mutex would only cover
// one server. Writers that edit files directly bypass the allocator; no id
// scheme protects that path.

func storeLockPath(p RepoPaths) string {
	return filepath.Join(p.Data, ".idlock")
}

// withStoreLock runs fn while holding the store's advisory lock. flock locks
// belong to open file descriptions, so concurrent mints inside one process
// serialize exactly like concurrent mints across processes.
func withStoreLock(p RepoPaths, fn func()) error {
	if err := os.MkdirAll(p.Data, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(storeLockPath(p), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	fn()
	return nil
}

func idCountersPath(p RepoPaths) string {
	return filepath.Join(p.Data, "counters.json")
}

func readIDCounters(p RepoPaths) map[string]int {
	out := map[string]int{}
	data, err := os.ReadFile(idCountersPath(p))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func writeIDCounters(p RepoPaths, counters map[string]int) {
	if err := os.MkdirAll(p.Data, 0755); err != nil {
		return
	}
	data, _ := json.Marshal(counters)
	_ = os.WriteFile(idCountersPath(p), data, 0644)
}

// maxIDOnDisk scans a kind dir for <prefix>-<n><ext> files (or bare <n><ext>
// for numeric kinds like tickets) and returns the highest n found.
func maxIDOnDisk(root, prefix, ext string) int {
	pattern := "*.md*"
	if ext != "" {
		pattern = "*" + ext
	}
	files, _ := filepath.Glob(filepath.Join(root, pattern))
	maxN := 0
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		var n int
		if prefix == "" {
			if _, err := fmt.Sscanf(stem, "%d", &n); err == nil && n > maxN {
				maxN = n
			}
			continue
		}
		if !strings.HasPrefix(stem, prefix+"-") {
			continue
		}
		rest := strings.TrimPrefix(stem, prefix+"-")
		if i := strings.IndexByte(rest, '-'); i >= 0 {
			rest = rest[:i]
		}
		if v, err := strconv.Atoi(rest); err == nil && v > maxN {
			maxN = v
		}
	}
	return maxN
}

// idPrefixFor returns the minting prefix of an artifact kind: the configured
// id_prefix, or "" for numeric-only kinds (tickets), which mint bare numbers.
func idPrefixFor(cfg EngineConfig, kindName string) string {
	if k, ok := cfg.ArtifactKinds[kindName]; ok {
		return k.IDPrefix
	}
	return ""
}

// mintArtifactID allocates the next id of a kind for a repo, under the store
// lock: strictly greater than every id on disk and every id ever minted for
// that kind, persisted before returning.
func mintArtifactID(cfg EngineConfig, p RepoPaths, kindName string) string {
	k, ok := cfg.ArtifactKinds[kindName]
	if !ok {
		return ""
	}
	ext := k.Extension
	prefix := idPrefixFor(cfg, kindName)
	var id string
	if err := withStoreLock(p, func() {
		root := artifactKindRootFor(p, k)
		if err := os.MkdirAll(root, 0755); err != nil {
			return
		}
		n := maxIDOnDisk(root, prefix, ext)
		counters := readIDCounters(p)
		if stored := counters[kindName]; stored > n {
			n = stored
		}
		n++
		counters[kindName] = n
		writeIDCounters(p, counters)
		if prefix == "" {
			id = strconv.Itoa(n)
		} else {
			id = fmt.Sprintf("%s-%d", prefix, n)
		}
	}); err != nil {
		return ""
	}
	return id
}

// previewMintArtifactID computes the id the next mint would return without
// allocating it: max(disk ids, persisted counter) + 1, under the store lock
// so the preview never races a real mint. Authorization uses the preview to
// resolve the would-be path of an empty-id write; the handler's real mint is
// the only allocator, so the preview consumes nothing.
func previewMintArtifactID(cfg EngineConfig, p RepoPaths, kindName string) string {
	k, ok := cfg.ArtifactKinds[kindName]
	if !ok {
		return ""
	}
	ext := k.Extension
	prefix := idPrefixFor(cfg, kindName)
	var id string
	if err := withStoreLock(p, func() {
		root := artifactKindRootFor(p, k)
		n := maxIDOnDisk(root, prefix, ext)
		if stored := readIDCounters(p)[kindName]; stored > n {
			n = stored
		}
		n++
		if prefix == "" {
			id = strconv.Itoa(n)
		} else {
			id = fmt.Sprintf("%s-%d", prefix, n)
		}
	}); err != nil {
		return ""
	}
	return id
}

// lessArtifactID orders ids naturally: digit runs compare numerically, text
// runs compare lexicographically. REQ-2 sorts before REQ-10, and unpadded
// (REQ-2) and padded (REQ-002) ids of the same value interleave correctly.
func lessArtifactID(a, b string) bool {
	ai, bi := []rune(a), []rune(b)
	for i, j := 0, 0; i < len(ai) || j < len(bi); {
		da := i < len(ai) && ai[i] >= '0' && ai[i] <= '9'
		db := j < len(bi) && bi[j] >= '0' && bi[j] <= '9'
		if da && db {
			na, ni := digitRun(ai, i)
			nb, nj := digitRun(bi, j)
			if na != nb {
				return na < nb
			}
			i, j = ni, nj
			continue
		}
		if da != db {
			return da // digits sort before text
		}
		if i >= len(ai) || j >= len(bi) {
			break
		}
		if ai[i] != bi[j] {
			return ai[i] < bi[j]
		}
		i++
		j++
	}
	return len(ai) < len(bi)
}

func digitRun(s []rune, i int) (int, int) {
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	// Leading zeros do not count: compare by trimmed value.
	trimmed := strings.TrimLeft(string(s[i:j]), "0")
	if trimmed == "" {
		return 0, j
	}
	n, _ := strconv.Atoi(trimmed)
	return n, j
}

func sortArtifactIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool { return lessArtifactID(ids[i], ids[j]) })
}
