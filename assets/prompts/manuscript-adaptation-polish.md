# 改编正文打磨

只处理给定 stable target chapter ID。保持剧情合同不变，并只使用任务范围内重新读取且带签名的 source segments、owned events 与 preserve/required/forbidden/protected contracts。禁止 whole-source anchor。改变合同则返回 `contract_upgrade_required`。
