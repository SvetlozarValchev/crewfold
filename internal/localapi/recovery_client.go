package localapi

import (
	"context"
	"time"
)

const recoveryMaintenanceTimeout = 60 * time.Second

func (c *Client) SystemDoctorFull(ctx context.Context) (FullDoctorResult, error) {
	var result FullDoctorResult
	if err := c.callParamsStrictWithTimeout(ctx, recoveryMaintenanceTimeout, MethodSystemDoctorFull, SystemDoctorFullParams{}, &result); err != nil {
		return FullDoctorResult{}, err
	}
	return result, nil
}

func (c *Client) BackupCreate(ctx context.Context, params BackupCreateParams) (BackupCreateResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result BackupCreateResult
	if err := c.callParamsStrictWithTimeout(ctx, recoveryMaintenanceTimeout, MethodBackupCreate, params, &result); err != nil {
		return BackupCreateResult{}, err
	}
	return result, nil
}
