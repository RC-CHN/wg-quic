#!/usr/bin/env python3
"""Add Chinese captions below the app and encode the optional UI demo to MP4."""
import json
from pathlib import Path
import subprocess
import sys

output = Path(sys.argv[1] if len(sys.argv) > 1 else 'dist/desktop-redesign').resolve()
timeline = json.loads((output / 'chapters.json').read_text())

def timestamp(seconds):
    centiseconds = round(seconds * 100)
    return f'{centiseconds // 360000}:{centiseconds // 6000 % 60:02}:{centiseconds // 100 % 60:02}.{centiseconds % 100:02}'

header = '''[Script Info]
ScriptType: v4.00+
PlayResX: 1180
PlayResY: 880
WrapStyle: 0
[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Caption,Noto Sans CJK SC,22,&H00FFFFFF,&H00FFFFFF,&H00111827,&H00111827,0,0,0,0,100,100,0,0,1,0,0,2,28,28,22,1
Style: Label,Noto Sans CJK SC,14,&H00BBC5D6,&H00BBC5D6,&H00111827,&H00111827,0,0,0,0,100,100,0,0,1,0,0,7,20,20,770,1
[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
'''
lines = [f'Dialogue: 0,0:00:00.00,{timestamp(timeline["duration"])},Label,,0,0,0,,wg-quic 桌面重设计 · 实际界面操作 · 隔离模拟数据']
for index, chapter in enumerate(timeline['chapters']):
    end = timeline['chapters'][index + 1]['seconds'] if index + 1 < len(timeline['chapters']) else timeline['duration']
    text = chapter['text'].replace('\\', '').replace('{', '').replace('}', '').replace('\n', r'\N')
    lines.append(f'Dialogue: 1,{timestamp(chapter["seconds"])},{timestamp(end)},Caption,,0,0,0,,{text}')
(output / 'demo.ass').write_text(header + '\n'.join(lines) + '\n')
# A relative filter path avoids ffmpeg's special path escaping rules.
subprocess.run(['ffmpeg', '-y', '-loglevel', 'warning', '-i', 'demo-raw.webm', '-vf',
                'pad=iw:ih+120:0:0:color=0x111827,ass=demo.ass', '-c:v', 'libx264',
                '-preset', 'medium', '-crf', '18', '-pix_fmt', 'yuv420p', '-movflags',
                '+faststart', '-an', 'demo.mp4'], cwd=output, check=True)
print(output / 'demo.mp4')
