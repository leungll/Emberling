"""Export the presentation diagrams from their editable candidates."""
from pathlib import Path
import runpy
runpy.run_path(str(Path(__file__).with_name("render-engineering-diagrams.py")), run_name="__main__")
