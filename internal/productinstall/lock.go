package productinstall

import (
	"context"
	"fmt"
)

func (service *Service) authorizeMutation(ctx context.Context, authority MutationAuthority, action Action) error {
	if err := validateAuthority(authority, action); err != nil {
		return err
	}
	if err := service.deps.Authorizer.Authorize(ctx, authority, action); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityRequired, err)
	}
	return nil
}
