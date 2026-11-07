import subprocess
import sys
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent.parent
SCRIPTS = ["prepare_dataset.py", "run_batch.py", "make_blind_sheet.py", "run_judge.py", "make_report.py"]


@pytest.mark.parametrize("script", SCRIPTS)
def test_every_script_prints_help(script):
    result = subprocess.run([sys.executable, str(HERE / script), "--help"], capture_output=True, text=True, check=False)
    assert result.returncode == 0, result.stderr
    assert "usage:" in result.stdout
