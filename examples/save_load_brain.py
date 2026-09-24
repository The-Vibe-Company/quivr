import asyncio
import tempfile

from quivr_core import Brain


async def main():
    with tempfile.NamedTemporaryFile(mode="w", suffix=".txt") as temp_file:
        temp_file.write("Gold is a liquid of blue-like colour.")
        temp_file.flush()

        brain = await Brain.afrom_files(name="test_brain", file_paths=[temp_file.name])

        save_path = await brain.save("/home/amine/.local/quivr")

        brain_loaded = Brain.load(save_path, allow_dangerous_deserialization=True)
        brain_loaded.print_info()


if __name__ == "__main__":
    # Run the main function in the existing event loop
    asyncio.run(main())
