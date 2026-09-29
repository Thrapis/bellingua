@echo off
rem Run the Lingvanex translation server standalone. bellingua serve starts it
rem by itself (lingvanex.manage, on by default), so this is only for debugging.
cd /d "%~dp0"
py ./server.py
