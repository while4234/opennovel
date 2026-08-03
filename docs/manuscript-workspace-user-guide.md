# 稿件工作区用户指南

1. 在稿件树按 stable ID 选择正文、提纲、审核或历史视图。显示序号可能因插章变化，但 stable ID 不变。
2. current 是已发布正式稿；candidate 是隔离候选。候选失败、拒绝或过期不会覆盖 current。
3. 修改正文或结构后先查看影响 preview，再确认推荐影响范围。发布前需要签名审核和人工确认。
4. `preview_stale` 表示 current/结构/签名已经变化，请刷新后重新预览；`revision_conflict` 表示另一写入先完成；`publication_recovery_required` 表示当前只读，等待恢复完成。
5. 恢复历史版本不会直接覆盖 current，而是创建新的 revision impact preview。
6. 完结作品追加或返工后，系统必须重新执行正文、postprocess、弧/卷/全书和既有完成态检查；失败会保留人工节点，resume 不会自动越过。
7. normal 项目不会接收 source/adaptation 字段；adaptation 视图同时显示 target display 和只读 source lineage，两者不可混用。

导出只包含 approved current，并遵循正式稿件树的当前显示顺序。TXT/EPUB 的 From/To 是显示位置，不是永久身份。
