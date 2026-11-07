package library

import (
	"context"
	"net/http"

	"github.com/snowmerak/q/internal/bundledskills"
	"github.com/snowmerak/q/internal/skillbundle"
)

func (s *skillService) syncBundle(ctx context.Context, bundle skillbundle.Bundle) (skillbundle.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest, err := skillbundle.Install(s.dir, bundle)
	if err != nil {
		return skillbundle.Manifest{}, err
	}
	if current := s.bundle.Load(); current != nil && current.Digest == manifest.Digest {
		return manifest, nil
	}
	if err := s.registry.Reload(); err != nil {
		return skillbundle.Manifest{}, err
	}
	if err := s.registry.SyncRecords(ctx, s.archive); err != nil {
		return skillbundle.Manifest{}, err
	}
	s.bundle.Store(&manifest)
	return manifest, nil
}

func (c *Client) syncBundledSkills(ctx context.Context, health Health) error {
	if !health.BundledSkills {
		return nil
	} // Older compatible servers keep serving existing skills until restarted.
	bundle := bundledskills.CDP()
	manifest, err := bundle.Manifest()
	if err != nil {
		return err
	}
	if health.SkillBundleDigest == manifest.Digest || health.SkillBundleVersion > manifest.Version {
		return nil
	}
	var output skillbundle.Manifest
	return c.doJSONWithClient(ctx, c.index, http.MethodPut, "/skills/bundled/cdp", bundle, &output, nil)
}

func (s *skillService) bundleHealth(health Health) Health {
	if manifest := s.bundle.Load(); manifest != nil {
		health.SkillBundleDigest, health.SkillBundleVersion = manifest.Digest, manifest.Version
	}
	return health
}
