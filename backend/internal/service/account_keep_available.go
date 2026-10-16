package service

import (
	"context"
	"errors"
	"log/slog"
)

// KeepAvailableExtraKey 账号「保持可用」开关在 extra 中的键。
//
// 开启后，无论上游返回什么（错误码、限流、过载、网络层故障、流内失败），
// 系统都不会自动把账号置为错误 / 限流 / 过载 / 临时不可调度 / 模型级限流，
// 调度时也忽略这些冷却状态与进程内的临时封禁。
// 管理员自己的配置仍然生效：手动停用、手动关闭调度、过期自动暂停、额度上限。
const KeepAvailableExtraKey = "keep_available"

// KeepAvailable 报告账号是否开启了「保持可用」。
func (a *Account) KeepAvailable() bool {
	if a == nil {
		return false
	}
	return a.getExtraBool(KeepAvailableExtraKey)
}

// restoreKeepAvailableAccountState 在开启「保持可用」时清掉账号已有的自动冷却/错误状态，
// 手动停用与调度开关保持原值。历史 error 账号没有记录调度暂停的来源，
// 因此只恢复 status；schedulable=false 的账号仍需管理员显式打开调度。
func (s *adminServiceImpl) restoreKeepAvailableAccountState(ctx context.Context, account *Account) error {
	if account == nil || !account.KeepAvailable() {
		return nil
	}
	id := account.ID
	repo := s.accountRestoreRepo
	if repo == nil {
		repo, _ = s.accountRepo.(AccountKeepAvailableRepository)
	}
	if repo == nil {
		return errors.New("account repository does not support keep-available restoration")
	}
	applied, err := repo.RestoreKeepAvailableState(ctx, id)
	if err != nil || !applied {
		return err
	}
	if s.runtimeBlocker != nil {
		s.runtimeBlocker.ClearAccountSchedulingBlock(id)
	}
	slog.Info("account_keep_available_state_restored", "account_id", id)
	return nil
}
