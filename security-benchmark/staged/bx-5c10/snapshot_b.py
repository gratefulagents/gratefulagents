def execute(data):
    ok = data["signature_valid"] and data["signed_chain"] == data["current_chain"]
    return {"authorized": ok}
