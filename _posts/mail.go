package main

import "fmt"

type ListNode struct {
	Val  int
	Next *ListNode
}

func reorderList(head *ListNode) {
	// write code here
	cnt := 0
	// wait for insert refind
	OriHead := head
	fmt.Println(cnt)
	for head.Next != nil {
		head = head.Next
		cnt++
	}
	half := OriHead
	for cnt/2 > 0 {
		cnt--
		half = half.Next
	}
	fmt.Println(cnt)
	// half 代表example中的2，即第一段的末尾。

	// o(1)翻转
	// 翻转 half.Next到末尾，末尾即原先的Head
	// Null
	prev := head.Next
	// 第二段的开头
	sta := half.Next
	for prev != head {
		next := sta.Next
		sta.Next = prev
		prev = sta
		sta = next
	}

	insertedHead := OriHead
	// prev是第二段的头
	for prev != nil {
		next := insertedHead.Next
		insertedHead.Next = prev
		prev = prev.Next
		insertedHead.Next.Next = next
	}
	half.Next = nil

	return
}
