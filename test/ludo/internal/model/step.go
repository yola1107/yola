package model

// Step 代表一次移动的步骤结果
type Step struct {
	ID     int32        // 棋子ID
	X      int32        // 移动步数
	From   int32        // 起始位置
	To     int32        // 目标位置
	Color  int32        // 棋子颜色
	Killed []KilledInfo // 击杀信息
}

type KilledInfo struct {
	ID    int32
	From  int32
	To    int32
	Color int32
}
