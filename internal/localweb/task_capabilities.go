package localweb

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func taskNativeCapabilities(ctx context.Context, reader storecontract.Reader, dataRoot string, task domain.Task, projectID domain.ProjectID) (nativecapabilities.Config, error) {
	settings, err := reader.GetTaskExecutionSettings(ctx, task.ID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return nativecapabilities.Read(dataRoot, projectID.String())
	}
	if err != nil {
		return nativecapabilities.Config{}, err
	}
	if settings.ProjectID != projectID {
		return nativecapabilities.Config{}, errors.New("task capability Project differs from its execution settings")
	}
	var config nativecapabilities.Config
	if err = json.Unmarshal([]byte(settings.NativeCapabilitiesJSON), &config); err != nil {
		return nativecapabilities.Config{}, errors.New("task native capability settings are invalid")
	}
	return config, nil
}
