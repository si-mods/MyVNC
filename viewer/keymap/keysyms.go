// Derived from TigerVNC common/rfb/keysymdef.h, common/rfb/XF86keysym.h and
// the Windows virtual-key codes used by vncviewer/KeyboardWin32.cxx
// SPDX-License-Identifier: GPL-2.0-or-later

package keymap

// NoSymbol is the absent keysym.
const NoSymbol = 0

// X11 keysyms used by the translation.
const (
	XK_BackSpace         = 0xff08
	XK_Tab               = 0xff09
	XK_Clear             = 0xff0b
	XK_Return            = 0xff0d
	XK_Pause             = 0xff13
	XK_Scroll_Lock       = 0xff14
	XK_Sys_Req           = 0xff15
	XK_Escape            = 0xff1b
	XK_Kanji             = 0xff21
	XK_Muhenkan          = 0xff22
	XK_Henkan            = 0xff23
	XK_Romaji            = 0xff24
	XK_Hiragana          = 0xff25
	XK_Katakana          = 0xff26
	XK_Hiragana_Katakana = 0xff27
	XK_Zenkaku_Hankaku   = 0xff2a
	XK_Eisu_toggle       = 0xff30
	XK_Hangul            = 0xff31
	XK_Hangul_Hanja      = 0xff34
	XK_Home              = 0xff50
	XK_Left              = 0xff51
	XK_Up                = 0xff52
	XK_Right             = 0xff53
	XK_Down              = 0xff54
	XK_Prior             = 0xff55
	XK_Next              = 0xff56
	XK_End               = 0xff57
	XK_Print             = 0xff61
	XK_Insert            = 0xff63
	XK_Menu              = 0xff67
	XK_Break             = 0xff6b
	XK_Num_Lock          = 0xff7f
	XK_KP_Enter          = 0xff8d
	XK_KP_Home           = 0xff95
	XK_KP_Left           = 0xff96
	XK_KP_Up             = 0xff97
	XK_KP_Right          = 0xff98
	XK_KP_Down           = 0xff99
	XK_KP_Prior          = 0xff9a
	XK_KP_Next           = 0xff9b
	XK_KP_End            = 0xff9c
	XK_KP_Insert         = 0xff9e
	XK_KP_Delete         = 0xff9f
	XK_KP_Multiply       = 0xffaa
	XK_KP_Add            = 0xffab
	XK_KP_Separator      = 0xffac
	XK_KP_Subtract       = 0xffad
	XK_KP_Decimal        = 0xffae
	XK_KP_Divide         = 0xffaf
	XK_KP_0              = 0xffb0 // XK_KP_1..XK_KP_9 follow
	XK_F1                = 0xffbe // XK_F2..XK_F24 follow
	XK_Shift_L           = 0xffe1
	XK_Shift_R           = 0xffe2
	XK_Control_L         = 0xffe3
	XK_Control_R         = 0xffe4
	XK_Caps_Lock         = 0xffe5
	XK_Alt_L             = 0xffe9
	XK_Alt_R             = 0xffea
	XK_Super_L           = 0xffeb
	XK_Super_R           = 0xffec
	XK_Hyper_L           = 0xffed
	XK_Hyper_R           = 0xffee
	XK_Delete            = 0xffff
	XK_ISO_Level3_Shift  = 0xfe03
	XK_ISO_Left_Tab      = 0xfe20

	XF86XK_AudioLowerVolume = 0x1008ff11
	XF86XK_AudioMute        = 0x1008ff12
	XF86XK_AudioRaiseVolume = 0x1008ff13
	XF86XK_AudioPlay        = 0x1008ff14
	XF86XK_AudioStop        = 0x1008ff15
	XF86XK_AudioPrev        = 0x1008ff16
	XF86XK_AudioNext        = 0x1008ff17
	XF86XK_HomePage         = 0x1008ff18
	XF86XK_Mail             = 0x1008ff19
	XF86XK_Search           = 0x1008ff1b
	XF86XK_Calculator       = 0x1008ff1d
	XF86XK_Back             = 0x1008ff26
	XF86XK_Forward          = 0x1008ff27
	XF86XK_Stop             = 0x1008ff28
	XF86XK_Refresh          = 0x1008ff29
	XF86XK_Sleep            = 0x1008ff2f
	XF86XK_Favorites        = 0x1008ff30
	XF86XK_AudioMedia       = 0x1008ff32
	XF86XK_MyComputer       = 0x1008ff33
)

// Windows virtual-key codes (winuser.h).
const (
	VK_CANCEL              = 0x03
	VK_BACK                = 0x08
	VK_TAB                 = 0x09
	VK_CLEAR               = 0x0c
	VK_RETURN              = 0x0d
	VK_SHIFT               = 0x10
	VK_CONTROL             = 0x11
	VK_MENU                = 0x12
	VK_PAUSE               = 0x13
	VK_CAPITAL             = 0x14
	VK_KANA                = 0x15
	VK_HANGUL              = 0x15
	VK_HANJA               = 0x19
	VK_KANJI               = 0x19
	VK_ESCAPE              = 0x1b
	VK_CONVERT             = 0x1c
	VK_NONCONVERT          = 0x1d
	VK_SPACE               = 0x20
	VK_PRIOR               = 0x21
	VK_NEXT                = 0x22
	VK_END                 = 0x23
	VK_HOME                = 0x24
	VK_LEFT                = 0x25
	VK_UP                  = 0x26
	VK_RIGHT               = 0x27
	VK_DOWN                = 0x28
	VK_SNAPSHOT            = 0x2c
	VK_INSERT              = 0x2d
	VK_DELETE              = 0x2e
	VK_LWIN                = 0x5b
	VK_RWIN                = 0x5c
	VK_APPS                = 0x5d
	VK_SLEEP               = 0x5f
	VK_NUMPAD0             = 0x60 // VK_NUMPAD1..VK_NUMPAD9 follow
	VK_MULTIPLY            = 0x6a
	VK_ADD                 = 0x6b
	VK_SEPARATOR           = 0x6c
	VK_SUBTRACT            = 0x6d
	VK_DECIMAL             = 0x6e
	VK_DIVIDE              = 0x6f
	VK_F1                  = 0x70 // VK_F2..VK_F24 follow
	VK_NUMLOCK             = 0x90
	VK_SCROLL              = 0x91
	VK_LSHIFT              = 0xa0
	VK_RSHIFT              = 0xa1
	VK_LCONTROL            = 0xa2
	VK_RCONTROL            = 0xa3
	VK_LMENU               = 0xa4
	VK_RMENU               = 0xa5
	VK_BROWSER_BACK        = 0xa6
	VK_BROWSER_FORWARD     = 0xa7
	VK_BROWSER_REFRESH     = 0xa8
	VK_BROWSER_STOP        = 0xa9
	VK_BROWSER_SEARCH      = 0xaa
	VK_BROWSER_FAVORITES   = 0xab
	VK_BROWSER_HOME        = 0xac
	VK_VOLUME_MUTE         = 0xad
	VK_VOLUME_DOWN         = 0xae
	VK_VOLUME_UP           = 0xaf
	VK_MEDIA_NEXT_TRACK    = 0xb0
	VK_MEDIA_PREV_TRACK    = 0xb1
	VK_MEDIA_STOP          = 0xb2
	VK_MEDIA_PLAY_PAUSE    = 0xb3
	VK_LAUNCH_MAIL         = 0xb4
	VK_LAUNCH_MEDIA_SELECT = 0xb5
	VK_LAUNCH_APP1         = 0xb6
	VK_LAUNCH_APP2         = 0xb7
	VK_PACKET              = 0xe7
	VK_OEM_ATTN            = 0xf0
	VK_OEM_FINISH          = 0xf1
	VK_OEM_COPY            = 0xf2
	VK_OEM_AUTO            = 0xf3
	VK_OEM_ENLW            = 0xf4
	VK_OEM_BACKTAB         = 0xf5
	VK_ATTN                = 0xf6
)

// Primary language IDs (winnt.h) with their own vkey tables.
const (
	LANG_JAPANESE = 0x11
	LANG_KOREAN   = 0x12
)
